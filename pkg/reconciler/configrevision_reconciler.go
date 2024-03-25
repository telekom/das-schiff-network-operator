package reconciler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	StatusInvalid      = "invalid"
	StatusProvisioning = "provisioning"
	StatusProvisioned  = "provisioned"

	controlPlaneLabel = "node-role.kubernetes.io/control-plane"
	numOfRefs         = 2
	configTimeout     = time.Minute * 2

	numOfDeploymentRetries = 3
)

// ConfigRevisionReconciler is responsible for creating NodeConfig objects.
type ConfigRevisionReconciler struct {
	logger      logr.Logger
	debouncer   *debounce.Debouncer
	client      client.Client
	timeout     time.Duration
	scheme      *runtime.Scheme
	maxUpdating int
}

// Reconcile starts reconciliation.
func (crr *ConfigRevisionReconciler) Reconcile(ctx context.Context) {
	crr.debouncer.Debounce(ctx)
}

// // NewNodeConfigReconciler creates new reconciler that creates NodeConfig objects.
func NewNodeConfigReconciler(clusterClient client.Client, logger logr.Logger, timeout time.Duration, s *runtime.Scheme, maxUpdating int) (*ConfigRevisionReconciler, error) {
	reconciler := &ConfigRevisionReconciler{
		logger:      logger,
		timeout:     timeout,
		client:      clusterClient,
		scheme:      s,
		maxUpdating: maxUpdating,
	}

	reconciler.debouncer = debounce.NewDebouncer(reconciler.reconcileDebounced, defaultDebounceTime, logger)

	return reconciler, nil
}

func (crr *ConfigRevisionReconciler) reconcileDebounced(ctx context.Context) error {
	revisions, err := listRevisions(ctx, crr.client)
	if err != nil {
		return fmt.Errorf("error listing revisions: %w", err)
	}

	nodes, err := listNodes(ctx, crr.client)
	if err != nil {
		return fmt.Errorf("error listing nodes: %w", err)
	}

	nodeConfigs, err := crr.listConfigs(ctx)
	if err != nil {
		return fmt.Errorf("error listing configs: %w", err)
	}

	totalNodes := len(nodes)
	cntMap := map[string]*counters{}
	for i := range revisions.Items {
		var cnt *counters
		var err error
		if cnt, err = crr.processConfigsForRevision(ctx, nodeConfigs.Items, &revisions.Items[i]); err != nil {
			return fmt.Errorf("failed to process configs for revision %s: %w", revisions.Items[i].Name, err)
		}
		cntMap[revisions.Items[i].Spec.Revision] = cnt
	}

	revisionToDeploy := getFirstValidRevision(revisions.Items)

	nodesToDeploy := getOutdatedNodes(nodes, nodeConfigs.Items, revisionToDeploy)

	if err := crr.updateRevisionCounters(ctx, revisions.Items, revisionToDeploy, len(nodesToDeploy), totalNodes, cntMap); err != nil {
		return fmt.Errorf("failed to update queue counters: %w", err)
	}

	// there is nothing to deploy - skip
	if revisionToDeploy == nil {
		crr.logger.Error(fmt.Errorf("there is no revision to deploy"), "revision deployment aboorted")
		return nil
	}

	if revisionToDeploy.Status.Ongoing < crr.maxUpdating && len(nodesToDeploy) > 0 {
		if err := crr.deployNodeConfig(ctx, nodesToDeploy[0], revisionToDeploy); err != nil {
			return fmt.Errorf("error deploying node configurations: %w", err)
		}
	}

	// remove all but last known valid revision
	if err := crr.revisionCleanup(ctx); err != nil {
		return fmt.Errorf("error cleaning redundant revisions: %w", err)
	}

	return nil
}

func getFirstValidRevision(revisions []v1alpha1.NetworkConfigRevision) *v1alpha1.NetworkConfigRevision {
	i := slices.IndexFunc(revisions, func(r v1alpha1.NetworkConfigRevision) bool {
		return !r.Status.IsInvalid
	})
	if i > -1 {
		return &revisions[i]
	}
	return nil
}

type counters struct {
	ready, ongoing, invalid int
}

func (crr *ConfigRevisionReconciler) processConfigsForRevision(ctx context.Context, configs []v1alpha1.NodeNetworkConfig, revision *v1alpha1.NetworkConfigRevision) (*counters, error) {
	configs, err := crr.removeRedundantConfigs(ctx, configs)
	if err != nil {
		return nil, fmt.Errorf("failed to remove redundant configs: %w", err)
	}
	ready, ongoing, invalid := getRevisionCounters(configs, revision)
	cnt := &counters{ready: ready, ongoing: ongoing, invalid: invalid}

	if invalid > 0 {
		if err := crr.invalidateRevision(ctx, revision, "NetworkConfigRevision results in invalid config"); err != nil {
			return cnt, fmt.Errorf("faild to invalidate revision %s: %w", revision.Name, err)
		}
	}

	return cnt, nil
}

func getRevisionCounters(configs []v1alpha1.NodeNetworkConfig, revision *v1alpha1.NetworkConfigRevision) (ready, ongoing, invalid int) {
	ready = 0
	ongoing = 0
	invalid = 0
	for i := range configs {
		if configs[i].Spec.Revision == revision.Spec.Revision {
			switch configs[i].Status.ConfigStatus {
			case StatusInvalid:
				// Increase 'invalid' counter so we know that there the revision results in invalid configs.
				invalid++
			case StatusProvisioning, "":
				// Update ongoing counter
				ongoing++
				if wasConfigTimeoutReached(&configs[i]) {
					// If timout was reached revision is invalid (but still counts as ongoing).
					invalid++
				}
			case StatusProvisioned:
				// Update ready counter
				ready++
			}
		}
	}
	return ready, ongoing, invalid
}

func (crr *ConfigRevisionReconciler) removeRedundantConfigs(ctx context.Context, configs []v1alpha1.NodeNetworkConfig) ([]v1alpha1.NodeNetworkConfig, error) {
	cfg := []v1alpha1.NodeNetworkConfig{}
	for i := range configs {
		// Every NodeNetworkConfig obejct should have 2 owner references - for NodeConfigRevision and for the Node. If there is only one owner reference,
		// it means that either node or revision were deleted, so the config itself can be deleted as well.
		if len(configs[i].ObjectMeta.OwnerReferences) < numOfRefs {
			crr.logger.Info("deleting redundant NodeNetworkConfig", "name", configs[i].Name)
			if err := crr.client.Delete(ctx, &configs[i]); err != nil && !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("error deleting redundant node config - %s: %w", configs[i].Name, err)
			}
		} else {
			cfg = append(cfg, configs[i])
		}
	}
	return cfg, nil
}

func (crr *ConfigRevisionReconciler) invalidateRevision(ctx context.Context, revision *v1alpha1.NetworkConfigRevision, reason string) error {
	crr.logger.Info("invalidating revision", "name", revision.Name, "reason", reason)
	revision.Status.IsInvalid = true
	if err := crr.client.Status().Update(ctx, revision); err != nil {
		return fmt.Errorf("failed to update revision status %s: %w", revision.Name, err)
	}
	return nil
}

func wasConfigTimeoutReached(cfg *v1alpha1.NodeNetworkConfig) bool {
	return time.Now().After(cfg.Status.LastUpdate.Add(configTimeout))
}

func getOutdatedNodes(nodes map[string]*corev1.Node, configs []v1alpha1.NodeNetworkConfig, revision *v1alpha1.NetworkConfigRevision) []*corev1.Node {
	if revision == nil {
		return []*corev1.Node{}
	}

	for nodeName := range nodes {
		for i := range configs {
			if configs[i].Name == nodeName && configs[i].Spec.Revision == revision.Spec.Revision {
				delete(nodes, nodeName)
				break
			}
		}
	}

	nodesToDeploy := []*corev1.Node{}
	for _, node := range nodes {
		nodesToDeploy = append(nodesToDeploy, node)
	}
	return nodesToDeploy
}

func (crr *ConfigRevisionReconciler) updateRevisionCounters(ctx context.Context, revisions []v1alpha1.NetworkConfigRevision, currentRevision *v1alpha1.NetworkConfigRevision, queued, totalNodes int, cnt map[string]*counters) error {
	for i := range revisions {
		q := 0
		if currentRevision != nil && revisions[i].Spec.Revision == currentRevision.Spec.Revision {
			q = queued
		}
		revisions[i].Status.Queued = q
		revisions[i].Status.Ongoing = cnt[revisions[i].Spec.Revision].ongoing
		revisions[i].Status.Ready = cnt[revisions[i].Spec.Revision].ready
		revisions[i].Status.Total = totalNodes
		if err := crr.client.Status().Update(ctx, &revisions[i]); err != nil {
			return fmt.Errorf("failed to update counters for revision %s: %w", revisions[i].Name, err)
		}
	}
	return nil
}

func (crr *ConfigRevisionReconciler) revisionCleanup(ctx context.Context) error {
	revisions, err := listRevisions(ctx, crr.client)
	if err != nil {
		return fmt.Errorf("failed to list revisions: %w", err)
	}

	if len(revisions.Items) > 1 {
		nodeConfigs, err := crr.listConfigs(ctx)
		if err != nil {
			return fmt.Errorf("failed to list configs: %w", err)
		}
		if !revisions.Items[0].Status.IsInvalid && revisions.Items[0].Status.Ready == revisions.Items[0].Status.Total {
			for i := 1; i < len(revisions.Items); i++ {
				if countReferences(&revisions.Items[i], nodeConfigs.Items) == 0 {
					crr.logger.Info("deleting NetworkConfigRevision", "name", revisions.Items[i].Name)
					if err := crr.client.Delete(ctx, &revisions.Items[i]); err != nil {
						return fmt.Errorf("failed to delete revision %s: %w", revisions.Items[i].Name, err)
					}
				}
			}
		}
	}

	return nil
}

func countReferences(revision *v1alpha1.NetworkConfigRevision, configs []v1alpha1.NodeNetworkConfig) int {
	refCnt := 0
	for j := range configs {
		if configs[j].Spec.Revision == revision.Spec.Revision {
			refCnt++
		}
	}
	return refCnt
}

func (crr *ConfigRevisionReconciler) listConfigs(ctx context.Context) (*v1alpha1.NodeNetworkConfigList, error) {
	nodeConfigs := &v1alpha1.NodeNetworkConfigList{}
	if err := crr.client.List(ctx, nodeConfigs); err != nil {
		return nil, fmt.Errorf("error listing NodeNetworkConfigs: %w", err)
	}
	return nodeConfigs, nil
}

func (crr *ConfigRevisionReconciler) deployNodeConfig(ctx context.Context, node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) error {
	currentConfig := &v1alpha1.NodeNetworkConfig{}
	if err := crr.client.Get(ctx, types.NamespacedName{Name: node.Name}, currentConfig); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("error getting NodeNetworkConfig object for node %s: %w", node.Name, err)
		}
		currentConfig = nil
	}

	if currentConfig != nil && currentConfig.Spec.Revision == revision.Spec.Revision {
		// current config is the same as current revision - skip
		return nil
	}

	newConfig, err := crr.createConfigForNode(node, revision)
	if err != nil {
		return fmt.Errorf("error preparing NodeNetworkConfig for node %s: %w", node.Name, err)
	}

	for i := 0; i < numOfDeploymentRetries; i++ {
		if err := crr.deployConfig(ctx, newConfig, currentConfig, node); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			return fmt.Errorf("error deploying NodeNetworkConfig for node %s: %w", node.Name, err)
		}
		break
	}

	crr.logger.Info("deployed NodeNetworkConfig", "name", newConfig.Name)

	return nil
}

func (crr *ConfigRevisionReconciler) createConfigForNode(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) (*v1alpha1.NodeNetworkConfig, error) {
	// create new config
	c := &v1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
		},
	}

	c.Spec = revision.Spec.Config
	c.Spec.Revision = revision.Spec.Revision
	c.Name = node.Name

	if err := controllerutil.SetOwnerReference(node, c, scheme.Scheme); err != nil {
		return nil, fmt.Errorf("error setting owner references (node): %w", err)
	}

	if err := controllerutil.SetOwnerReference(revision, c, crr.scheme); err != nil {
		return nil, fmt.Errorf("error setting owner references (revision): %w", err)
	}

	// prepare Layer2NetworkConfigurationSpec (l2Spec) for each node.
	// Each Layer2NetworkConfigurationSpec from l2Spec has node selector,
	// which should be used to add config to proper nodes.
	// Each Layer2NetworkConfigurationSpec that don't match the node selector
	// is removed.
	var err error
	c.Spec.Layer2 = slices.DeleteFunc(c.Spec.Layer2, func(s v1alpha1.Layer2NetworkConfigurationSpec) bool {
		if err != nil {
			// skip if any errors occurred
			return false
		}
		if s.NodeSelector == nil {
			// node selector is not defined for the spec.
			// Layer2 is global - just continue
			return false
		}

		// node selector of type v1.labelSelector has to be converted
		// to labels.Selector type to be used with controller-runtime client
		var selector labels.Selector
		selector, err = convertSelector(s.NodeSelector.MatchLabels, s.NodeSelector.MatchExpressions)
		if err != nil {
			return false
		}

		// remove currently processed Layer2NetworkConfigurationSpec if node does not match the selector
		return !selector.Matches(labels.Set(node.ObjectMeta.Labels))
	})

	if err != nil {
		return nil, fmt.Errorf("failed to delete redundant Layer2NetworkConfigurationSpec: %w", err)
	}

	// set config as next config for the node
	return c, nil
}

func convertSelector(matchLabels map[string]string, matchExpressions []metav1.LabelSelectorRequirement) (labels.Selector, error) {
	selector := labels.NewSelector()
	var reqs labels.Requirements

	for key, value := range matchLabels {
		requirement, err := labels.NewRequirement(key, selection.Equals, []string{value})
		if err != nil {
			return nil, fmt.Errorf("error creating MatchLabel requirement: %w", err)
		}
		reqs = append(reqs, *requirement)
	}

	for _, req := range matchExpressions {
		lowercaseOperator := selection.Operator(strings.ToLower(string(req.Operator)))
		requirement, err := labels.NewRequirement(req.Key, lowercaseOperator, req.Values)
		if err != nil {
			return nil, fmt.Errorf("error creating MatchExpression requirement: %w", err)
		}
		reqs = append(reqs, *requirement)
	}
	selector = selector.Add(reqs...)

	return selector, nil
}

func (crr *ConfigRevisionReconciler) deployConfig(ctx context.Context, newConfig, currentConfig *v1alpha1.NodeNetworkConfig, node *corev1.Node) error {
	deploymentCtx, deploymentCtxCancel := context.WithTimeout(ctx, crr.timeout)
	defer deploymentCtxCancel()
	var cfg *v1alpha1.NodeNetworkConfig
	if currentConfig != nil {
		cfg = currentConfig
		// there already is config for node - update
		cfg.Spec = newConfig.Spec
		cfg.ObjectMeta.OwnerReferences = newConfig.ObjectMeta.OwnerReferences
		cfg.Name = node.Name
		if err := crr.client.Update(deploymentCtx, cfg); err != nil {
			return fmt.Errorf("error updating NodeNetworkConfig for node %s: %w", node.Name, err)
		}
	} else {
		cfg = newConfig
		// there is no config for node - create one
		if err := crr.client.Create(deploymentCtx, cfg); err != nil {
			return fmt.Errorf("error creating NodeNetworkConfig for node %s: %w", node.Name, err)
		}
	}

	return nil
}

func listNodes(ctx context.Context, c client.Client) (map[string]*corev1.Node, error) {
	// list all nodes
	list := &corev1.NodeList{}
	if err := c.List(ctx, list); err != nil {
		return nil, fmt.Errorf("unable to list nodes: %w", err)
	}

	// discard control-plane and not-ready nodes
	nodes := map[string]*corev1.Node{}
	for i := range list.Items {
		_, isControlPlane := list.Items[i].Labels[controlPlaneLabel]
		if !isControlPlane {
			// discard nodes that are not in ready state
			for j := range list.Items[i].Status.Conditions {
				// TODO: Should taint node.kubernetes.io/not-ready be used instead of Conditions?
				if list.Items[i].Status.Conditions[j].Type == corev1.NodeReady &&
					list.Items[i].Status.Conditions[j].Status == corev1.ConditionTrue {
					nodes[list.Items[i].Name] = &list.Items[i]
					break
				}
			}
		}
	}

	return nodes, nil
}
