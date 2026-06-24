package operator

import (
	"context"
	"fmt"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	conditionResolved = "Resolved"
	conditionApplied  = "Applied"
	conditionReady    = "Ready"
)

// reconcileMirrorStatus updates the status of all MirrorTarget and MirrorSelector
// objects based on the currently deployed NodeNetworkConfigs.
func (crr *ConfigRevisionReconciler) reconcileMirrorStatus(ctx context.Context) error {
	selectors := &v1alpha1.MirrorSelectorList{}
	if err := crr.client.List(ctx, selectors); err != nil {
		return fmt.Errorf("error listing MirrorSelectors: %w", err)
	}
	targets := &v1alpha1.MirrorTargetList{}
	if err := crr.client.List(ctx, targets); err != nil {
		return fmt.Errorf("error listing MirrorTargets: %w", err)
	}
	if len(selectors.Items) == 0 && len(targets.Items) == 0 {
		return nil
	}

	configs, err := crr.listConfigs(ctx)
	if err != nil {
		return fmt.Errorf("error listing configs for mirror status: %w", err)
	}

	resolver, err := crr.buildMirrorSourceResolver(ctx)
	if err != nil {
		return err
	}

	targetByName := make(map[string]*v1alpha1.MirrorTarget, len(targets.Items))
	selectorCount := map[string]int{}
	for i := range targets.Items {
		targetByName[targets.Items[i].Name] = &targets.Items[i]
	}
	for i := range selectors.Items {
		selectorCount[selectors.Items[i].Spec.MirrorTarget.Name]++
	}

	if err := crr.updateTargetStatuses(ctx, targets.Items, selectorCount, configs.Items); err != nil {
		return err
	}
	return crr.updateSelectorStatuses(ctx, selectors.Items, targetByName, resolver, configs.Items)
}

func (crr *ConfigRevisionReconciler) updateTargetStatuses(ctx context.Context, targets []v1alpha1.MirrorTarget, selectorCount map[string]int, configs []v1alpha1.NodeNetworkConfig) error {
	for i := range targets {
		t := &targets[i]
		activeNodes := countTargetNodes(t, configs)

		newStatus := t.Status.DeepCopy()
		newStatus.ActiveSelectors = selectorCount[t.Name]
		newStatus.ActiveNodes = activeNodes

		ready := metav1.Condition{Type: conditionReady, ObservedGeneration: t.Generation, Status: metav1.ConditionTrue, Reason: "Configured", Message: "GRE tunnel configured on at least one node"}
		if activeNodes == 0 {
			ready.Status = metav1.ConditionFalse
			ready.Reason = "NotConfigured"
			ready.Message = "GRE tunnel is not configured on any node"
		}
		meta.SetStatusCondition(&newStatus.Conditions, ready)

		if equality.Semantic.DeepEqual(&t.Status, newStatus) {
			continue
		}
		t.Status = *newStatus
		if err := crr.client.Status().Update(ctx, t); err != nil {
			return fmt.Errorf("error updating MirrorTarget %s status: %w", t.Name, err)
		}
	}
	return nil
}

func (crr *ConfigRevisionReconciler) updateSelectorStatuses(ctx context.Context, selectors []v1alpha1.MirrorSelector, targetByName map[string]*v1alpha1.MirrorTarget, resolver *mirrorSourceResolver, configs []v1alpha1.NodeNetworkConfig) error {
	for i := range selectors {
		s := &selectors[i]
		target, targetFound := targetByName[s.Spec.MirrorTarget.Name]
		sourceKey, sourceFound := resolver.resolve(s.Spec.MirrorSource)
		resolved := targetFound && sourceFound

		// Applied means this selector's own mirror rule is actually programmed on
		// at least one node: an ACL pointing at the target's GRE tunnel, attached
		// to this selector's source. Merely having the target's tunnel present
		// (possibly created by another selector) is not enough.
		applied := resolved && countSelectorNodes(s, target, sourceKey, configs) > 0

		newStatus := s.Status.DeepCopy()
		meta.SetStatusCondition(&newStatus.Conditions, resolvedCondition(s.Generation, targetFound, sourceFound))
		meta.SetStatusCondition(&newStatus.Conditions, appliedCondition(s.Generation, applied))

		if equality.Semantic.DeepEqual(&s.Status, newStatus) {
			continue
		}
		s.Status = *newStatus
		if err := crr.client.Status().Update(ctx, s); err != nil {
			return fmt.Errorf("error updating MirrorSelector %s status: %w", s.Name, err)
		}
	}
	return nil
}

func resolvedCondition(generation int64, targetFound, sourceFound bool) metav1.Condition {
	cond := metav1.Condition{Type: conditionResolved, ObservedGeneration: generation, Status: metav1.ConditionTrue, Reason: "Resolved", Message: "MirrorTarget and MirrorSource references resolved"}
	switch {
	case !targetFound && !sourceFound:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "ReferencesNotFound"
		cond.Message = "referenced MirrorTarget and MirrorSource do not exist"
	case !targetFound:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "TargetNotFound"
		cond.Message = "referenced MirrorTarget does not exist"
	case !sourceFound:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "SourceNotFound"
		cond.Message = "referenced MirrorSource does not exist"
	}
	return cond
}

func appliedCondition(generation int64, applied bool) metav1.Condition {
	cond := metav1.Condition{Type: conditionApplied, ObservedGeneration: generation, Status: metav1.ConditionTrue, Reason: "Applied", Message: "mirror rules programmed on at least one node"}
	if !applied {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "Pending"
		cond.Message = "mirror rules are not yet programmed on any node"
	}
	return cond
}

// countTargetNodes returns how many NodeNetworkConfigs carry the target's GRE tunnel.
func countTargetNodes(target *v1alpha1.MirrorTarget, configs []v1alpha1.NodeNetworkConfig) int {
	greName := greInterfaceName(target.Name, target.Spec.Type)
	count := 0
	for i := range configs {
		fvrf, ok := configs[i].Spec.FabricVRFs[target.Spec.DestinationVrf]
		if !ok {
			continue
		}
		if _, ok := fvrf.GREs[greName]; ok {
			count++
		}
	}
	return count
}

// countSelectorNodes returns how many NodeNetworkConfigs carry the MirrorACL
// produced by this selector: an ACL on the selector's own source (Layer2 or
// fabric VRF) that points at the target's GRE tunnel with the selector's
// direction and traffic match. This is stricter than countTargetNodes - it does
// not count nodes where only the tunnel exists (e.g. created by another selector
// referencing the same target) but the selector's mirror source has no ACL.
func countSelectorNodes(sel *v1alpha1.MirrorSelector, target *v1alpha1.MirrorTarget, sourceKey string, configs []v1alpha1.NodeNetworkConfig) int {
	if target == nil {
		return 0
	}
	want := v1alpha1.MirrorACL{
		TrafficMatch:      sel.Spec.TrafficMatch,
		MirrorDestination: greInterfaceName(target.Name, target.Spec.Type),
		Direction:         sel.Spec.Direction,
	}
	count := 0
	for i := range configs {
		var acls []v1alpha1.MirrorACL
		switch sel.Spec.MirrorSource.Kind {
		case sourceKindLayer2:
			acls = configs[i].Spec.Layer2s[sourceKey].MirrorACLs
		case sourceKindVRF:
			acls = configs[i].Spec.FabricVRFs[sourceKey].MirrorACLs
		}
		if containsMirrorACL(acls, &want) {
			count++
		}
	}
	return count
}

func containsMirrorACL(acls []v1alpha1.MirrorACL, want *v1alpha1.MirrorACL) bool {
	for i := range acls {
		if equality.Semantic.DeepEqual(&acls[i], want) {
			return true
		}
	}
	return false
}

// mirrorSourceResolver maps a MirrorSource object reference to the
// NodeNetworkConfig map key its MirrorACL is attached to (Layer2s key for a
// Layer2NetworkConfiguration, FabricVRFs key for a VRFRouteConfiguration) and
// reports whether the reference points at an existing object.
type mirrorSourceResolver struct {
	layer2Keys map[string]string
	vrfKeys    map[string]string
}

func (crr *ConfigRevisionReconciler) buildMirrorSourceResolver(ctx context.Context) (*mirrorSourceResolver, error) {
	l2List := &v1alpha1.Layer2NetworkConfigurationList{}
	if err := crr.client.List(ctx, l2List); err != nil {
		return nil, fmt.Errorf("error listing Layer2NetworkConfigurations for mirror status: %w", err)
	}
	vrfList := &v1alpha1.VRFRouteConfigurationList{}
	if err := crr.client.List(ctx, vrfList); err != nil {
		return nil, fmt.Errorf("error listing VRFRouteConfigurations for mirror status: %w", err)
	}

	r := &mirrorSourceResolver{
		layer2Keys: make(map[string]string, len(l2List.Items)),
		vrfKeys:    make(map[string]string, len(vrfList.Items)),
	}
	for i := range l2List.Items {
		r.layer2Keys[l2List.Items[i].Name] = fmt.Sprintf("%d", l2List.Items[i].Spec.ID)
	}
	for i := range vrfList.Items {
		r.vrfKeys[vrfList.Items[i].Name] = vrfList.Items[i].Spec.VRF
	}
	return r, nil
}

// resolve returns the NodeNetworkConfig map key for the selector's source and
// whether the source reference points at an existing object.
func (r *mirrorSourceResolver) resolve(src corev1.TypedObjectReference) (key string, found bool) {
	switch src.Kind {
	case sourceKindLayer2:
		key, found = r.layer2Keys[src.Name]
	case sourceKindVRF:
		key, found = r.vrfKeys[src.Name]
	}
	return key, found
}
