package operator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
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

	DefaultConfigTimeout   = "2m"
	DefaultPreconfigTimout = "10m"

	numOfRefs = 2

	numOfDeploymentRetries = 3

	permitRoute = "permit"
)

type AddressFamily int

const (
	Both AddressFamily = iota
	IPv4
	IPv6
)

type ImportMode int

const (
	ImportModeImport ImportMode = iota
	ImportModeStaticRoute
)

// ConfigRevisionReconciler is responsible for creating NodeConfig objects.
type ConfigRevisionReconciler struct {
	logger           logr.Logger
	debouncer        *debounce.Debouncer
	vrfConfig        *config.Config
	client           client.Client
	apiTimeout       time.Duration
	configTimeout    time.Duration
	preconfigTimeout time.Duration
	scheme           *runtime.Scheme
	maxUpdating      int

	importMode ImportMode
}

// Reconcile starts reconciliation.
func (crr *ConfigRevisionReconciler) Reconcile(ctx context.Context) {
	crr.debouncer.Debounce(ctx)
}

// // NewNodeConfigReconciler creates new reconciler that creates NodeConfig objects.
func NewNodeConfigReconciler(clusterClient client.Client, logger logr.Logger, apiTimeout, configTimeout, preconfigTimeout time.Duration, s *runtime.Scheme, maxUpdating int, importMode ImportMode) (*ConfigRevisionReconciler, error) {
	reconciler := &ConfigRevisionReconciler{
		logger:           logger,
		apiTimeout:       apiTimeout,
		configTimeout:    configTimeout,
		preconfigTimeout: preconfigTimeout,
		client:           clusterClient,
		scheme:           s,
		maxUpdating:      maxUpdating,
		importMode:       importMode,
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	reconciler.vrfConfig = cfg

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
	failedNode              string
	failedMessage           string
	failedAt                metav1.Time
}

func (crr *ConfigRevisionReconciler) processConfigsForRevision(ctx context.Context, configs []v1alpha1.NodeNetworkConfig, revision *v1alpha1.NetworkConfigRevision) (*counters, error) {
	configs, err := crr.removeRedundantConfigs(ctx, configs)
	if err != nil {
		return nil, fmt.Errorf("failed to remove redundant configs: %w", err)
	}
	cnt := crr.getRevisionCounters(configs, revision)

	if cnt.invalid > 0 {
		if err := crr.invalidateRevision(ctx, revision, cnt.failedNode, cnt.failedMessage, cnt.failedAt); err != nil {
			return cnt, fmt.Errorf("faild to invalidate revision %s: %w", revision.Name, err)
		}
	}

	return cnt, nil
}

func (crr *ConfigRevisionReconciler) getRevisionCounters(configs []v1alpha1.NodeNetworkConfig, revision *v1alpha1.NetworkConfigRevision) *counters {
	cnt := &counters{
		ready:   0,
		ongoing: 0,
		invalid: 0,
	}
	for i := range configs {
		if configs[i].Spec.Revision == revision.Spec.Revision {
			timeout := crr.configTimeout
			switch configs[i].Status.ConfigStatus {
			case StatusProvisioned:
				// Update ready counter
				cnt.ready++
			case StatusInvalid:
				// Increase 'invalid' counter so we know that the revision results in invalid configs
				cnt.invalid++
				// Capture the failure info (first one wins since rollout stops)
				if cnt.failedNode == "" {
					cnt.failedNode = configs[i].Name
					cnt.failedMessage = configs[i].Status.ErrorMessage
					cnt.failedAt = configs[i].Status.LastUpdate
				}
			case "":
				// Set longer timeout if status was not yet updated
				timeout = crr.preconfigTimeout
				fallthrough
			case StatusProvisioning:
				// Update ongoing counter
				cnt.ongoing++
				if wasConfigTimeoutReached(&configs[i], timeout) {
					// If timeout was reached revision is invalid (but still counts as ongoing).
					cnt.invalid++
					if cnt.failedNode == "" {
						cnt.failedNode = configs[i].Name
						cnt.failedMessage = "provisioning timeout reached"
						cnt.failedAt = configs[i].Status.LastUpdate
					}
				}
			}
		}
	}
	return cnt
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

func (crr *ConfigRevisionReconciler) invalidateRevision(ctx context.Context, revision *v1alpha1.NetworkConfigRevision, failedNode, failedMessage string, failedAt metav1.Time) error {
	crr.logger.Info("invalidating revision", "name", revision.Name, "failedNode", failedNode, "failedMessage", failedMessage)
	revision.Status.IsInvalid = true
	revision.Status.FailedNode = failedNode
	revision.Status.FailedMessage = failedMessage
	if !failedAt.IsZero() {
		revision.Status.FailedAt = &failedAt
	}

	if err := crr.client.Status().Update(ctx, revision); err != nil {
		return fmt.Errorf("failed to update revision status %s: %w", revision.Name, err)
	}
	return nil
}

func wasConfigTimeoutReached(cfg *v1alpha1.NodeNetworkConfig, timeout time.Duration) bool {
	if cfg.Status.LastUpdate.IsZero() {
		return false
	}
	return time.Now().After(cfg.Status.LastUpdate.Add(timeout))
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

	newConfig, err := crr.CreateNodeNetworkConfig(node, revision)
	if err != nil {
		return fmt.Errorf("error preparing NodeNetworkConfig for node %s: %w", node.Name, err)
	}

	for i := 0; i < numOfDeploymentRetries; i++ {
		if err := crr.deployNodeNetworkConfig(ctx, newConfig, currentConfig, node); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			return fmt.Errorf("error deploying NodeNetworkConfig for node %s: %w", node.Name, err)
		}
		break
	}

	// create netplan config
	if err := crr.createOrUpdateNetplanConfig(ctx, node, revision); err != nil {
		return fmt.Errorf("failed to deploy NodeNetworkConfig: %w", err)
	}

	crr.logger.Info("deployed NodeNetworkConfig", "name", newConfig.Name)

	return nil
}

func matchSelector(node *corev1.Node, selector *metav1.LabelSelector) bool {
	if selector == nil {
		return true
	}

	labelSelector, err := convertSelector(selector.MatchLabels, selector.MatchExpressions)
	if err != nil {
		return false
	}

	return labelSelector.Matches(labels.Set(node.ObjectMeta.Labels))
}

func (crr *ConfigRevisionReconciler) CreateNodeNetworkConfig(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) (*v1alpha1.NodeNetworkConfig, error) {
	// create new config
	c := &v1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
		},
	}

	if err := crr.vrfConfig.ReloadConfig(); err != nil {
		return nil, fmt.Errorf("error reloading config: %w", err)
	}

	if err := crr.buildNodeVrf(node, revision, c); err != nil {
		return nil, fmt.Errorf("error building node VRFs: %w", err)
	}
	if err := buildNodeLayer2(node, revision, c); err != nil {
		return nil, fmt.Errorf("error building node Layer2: %w", err)
	}
	if err := buildNodeBgpPeers(node, revision, c); err != nil {
		return nil, fmt.Errorf("error building node Layer2: %w", err)
	}

	c.Spec.Revision = revision.Spec.Revision
	c.Name = node.Name

	if err := controllerutil.SetOwnerReference(node, c, scheme.Scheme); err != nil {
		return nil, fmt.Errorf("error setting owner references (node): %w", err)
	}

	if err := controllerutil.SetOwnerReference(revision, c, crr.scheme); err != nil {
		return nil, fmt.Errorf("error setting owner references (revision): %w", err)
	}

	// set config as next config for the node
	return c, nil
}

func (crr *ConfigRevisionReconciler) createNodeNetplanConfig(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) (*v1alpha1.NodeNetplanConfig, error) {
	c := &v1alpha1.NodeNetplanConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
		},
		Spec: v1alpha1.NodeNetplanConfigSpec{
			DesiredState: netplan.State{
				Network: netplan.NetworkState{
					Version: 2, //nolint:mnd
				},
			},
		},
	}

	vlans, err := buildNetplanVLANs(node, revision)
	if err != nil {
		return nil, fmt.Errorf("error building netplan VLANs: %w", err)
	}
	c.Spec.DesiredState.Network.VLans = vlans

	dummies, err := buildNetplanDummies(node, revision)
	if err != nil {
		return nil, fmt.Errorf("error building netplan dummies: %w", err)
	}
	c.Spec.DesiredState.Network.Dummies = dummies

	if err := controllerutil.SetOwnerReference(node, c, scheme.Scheme); err != nil {
		return nil, fmt.Errorf("error setting owner references (node): %w", err)
	}

	if err := controllerutil.SetOwnerReference(revision, c, crr.scheme); err != nil {
		return nil, fmt.Errorf("error setting owner references (revision): %w", err)
	}

	return c, nil
}

func (crr *ConfigRevisionReconciler) createOrUpdateNetplanConfig(ctx context.Context, node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) error {
	currentNetplanConfig := &v1alpha1.NodeNetplanConfig{}
	if err := crr.client.Get(ctx, types.NamespacedName{Name: node.Name}, currentNetplanConfig); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("error getting NodeNetplanConfig object for node %s: %w", node.Name, err)
		}
		currentNetplanConfig = nil
	}
	netplanConfig, err := crr.createNodeNetplanConfig(node, revision)
	if err != nil {
		return fmt.Errorf("error creating NodeNetplanConfig for node %s: %w", node.Name, err)
	}
	if currentNetplanConfig == nil {
		if err := crr.client.Create(ctx, netplanConfig); err != nil {
			return fmt.Errorf("error creating NodeNetplanConfig for node %s: %w", node.Name, err)
		}
	} else {
		currentNetplanConfig.Spec = netplanConfig.Spec
		if err := crr.client.Update(ctx, currentNetplanConfig); err != nil {
			return fmt.Errorf("error updating NodeNetplanConfig for node %s: %w", node.Name, err)
		}
	}

	return nil
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

func (crr *ConfigRevisionReconciler) deployNodeNetworkConfig(ctx context.Context, newConfig, currentConfig *v1alpha1.NodeNetworkConfig, node *corev1.Node) error {
	deploymentCtx, deploymentCtxCancel := context.WithTimeout(ctx, crr.apiTimeout)
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

	return nodes, nil
}
