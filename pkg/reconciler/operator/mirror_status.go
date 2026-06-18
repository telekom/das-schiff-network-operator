package operator

import (
	"context"
	"fmt"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
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
	return crr.updateSelectorStatuses(ctx, selectors.Items, targetByName, configs.Items)
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

func (crr *ConfigRevisionReconciler) updateSelectorStatuses(ctx context.Context, selectors []v1alpha1.MirrorSelector, targetByName map[string]*v1alpha1.MirrorTarget, configs []v1alpha1.NodeNetworkConfig) error {
	for i := range selectors {
		s := &selectors[i]
		target, resolved := targetByName[s.Spec.MirrorTarget.Name]
		applied := resolved && countTargetNodes(target, configs) > 0

		newStatus := s.Status.DeepCopy()
		meta.SetStatusCondition(&newStatus.Conditions, resolvedCondition(s.Generation, resolved))
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

func resolvedCondition(generation int64, resolved bool) metav1.Condition {
	cond := metav1.Condition{Type: conditionResolved, ObservedGeneration: generation, Status: metav1.ConditionTrue, Reason: "Resolved", Message: "MirrorTarget reference resolved"}
	if !resolved {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "TargetNotFound"
		cond.Message = "referenced MirrorTarget does not exist"
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
