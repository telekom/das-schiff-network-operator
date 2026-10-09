package operator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
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

	nodeLocalRevisionAnnotation = "network.t-caas.telekom.com/node-local-revision"
	provisioningTimeoutMessage  = "provisioning timeout reached"
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

	// mirrorAllocCache memoises the per-node loopback allocation so that building
	// NodeNetworkConfigs node-by-node during a rollout does not recompute it (and
	// re-list all configs) for every single node.
	mirrorAllocCache mirrorAllocCache
}

// mirrorAllocCache caches a loopbackAllocator for a given (revision, ready-node
// set). The allocation is deterministic for those inputs, so it is safe to reuse
// across the per-node builds of a rollout. The ready-node set is part of the key
// because node membership does not change the revision hash, yet a newly-joined
// node must be picked up immediately (otherwise it would silently run without its
// mirror loopback/tunnel until an unrelated change bumped the revision).
type mirrorAllocCache struct {
	key   string
	alloc *loopbackAllocator
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

	nodesToDeploy, err := crr.getOutdatedNodes(ctx, nodes, nodeConfigs.Items, revisionToDeploy)
	if err != nil {
		return fmt.Errorf("error calculating desired node configurations: %w", err)
	}

	if err := crr.updateRevisionCounters(ctx, revisions.Items, revisionToDeploy, len(nodesToDeploy), totalNodes, cntMap); err != nil {
		return fmt.Errorf("failed to update queue counters: %w", err)
	}

	// there is nothing to deploy - skip
	if revisionToDeploy == nil {
		crr.logger.Error(fmt.Errorf("there is no revision to deploy"), "revision deployment aborted")
		return nil
	}

	if revisionToDeploy.Status.Ongoing < crr.maxUpdating && len(nodesToDeploy) > 0 {
		if err := crr.deployNodeConfig(ctx, nodesToDeploy[0]); err != nil {
			return fmt.Errorf("error deploying node configurations: %w", err)
		}
	}

	// Update MirrorTarget/MirrorSelector status from the deployed configs.
	if err := crr.reconcileMirrorStatus(ctx); err != nil {
		return fmt.Errorf("error reconciling mirror status: %w", err)
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
	revisionInvalid         bool
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

	if cnt.revisionInvalid {
		// Invalidate when transitioning to invalid state, or when the failed node
		// has changed (a different node may fail on an already-invalid revision).
		if !revision.Status.IsInvalid || revision.Status.FailedNode != cnt.failedNode {
			if err := crr.invalidateRevision(ctx, revision, cnt.failedNode, cnt.failedMessage, cnt.failedAt); err != nil {
				return cnt, fmt.Errorf("failed to invalidate revision %s: %w", revision.Name, err)
			}
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
		cfg := &configs[i]
		if cfg.Spec.Revision != revision.Spec.Revision {
			continue
		}
		status := cfg.Status.ConfigStatus
		if cfg.Spec.ConfigHash != cfg.Status.LastAppliedConfigHash &&
			(status == StatusProvisioned || status == StatusInvalid) {
			status = ""
		}

		timeout := crr.configTimeout
		switch status {
		case StatusProvisioned:
			// Update ready counter
			cnt.ready++
		case StatusInvalid:
			cnt.recordFailure(cfg, cfg.Status.ErrorMessage, cfg.Status.LastUpdate)
		case "":
			// Set longer timeout if status was not yet updated
			timeout = crr.preconfigTimeout
			fallthrough
		case StatusProvisioning:
			// Update ongoing counter
			cnt.ongoing++
			if wasConfigTimeoutReached(cfg, timeout) {
				cnt.recordFailure(cfg, provisioningTimeoutMessage, metav1.NewTime(configUpdateTime(cfg).Add(timeout)))
				if isNodeLocalConfig(cfg) {
					cnt.ongoing--
					crr.logger.Error(fmt.Errorf("%s", provisioningTimeoutMessage), "node-local configuration failed", "node", cfg.Name, "configHash", cfg.Spec.ConfigHash)
				}
			}
		}
	}
	return cnt
}

func (cnt *counters) recordFailure(cfg *v1alpha1.NodeNetworkConfig, message string, failedAt metav1.Time) {
	cnt.invalid++
	if isNodeLocalConfig(cfg) {
		return
	}
	cnt.revisionInvalid = true
	if cnt.failedNode == "" || cfg.Name < cnt.failedNode {
		cnt.failedNode, cnt.failedMessage, cnt.failedAt = cfg.Name, message, failedAt
	}
}

func isNodeLocalConfig(cfg *v1alpha1.NodeNetworkConfig) bool {
	revision, ok := cfg.Annotations[nodeLocalRevisionAnnotation]
	return ok && revision == cfg.Spec.Revision
}

func (crr *ConfigRevisionReconciler) removeRedundantConfigs(ctx context.Context, configs []v1alpha1.NodeNetworkConfig) ([]v1alpha1.NodeNetworkConfig, error) {
	cfg := []v1alpha1.NodeNetworkConfig{}
	for i := range configs {
		// Every NodeNetworkConfig object should have 2 owner references - for NodeConfigRevision and for the Node. If there is only one owner reference,
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
	lastUpdate := configUpdateTime(cfg)
	if lastUpdate.IsZero() {
		return false
	}
	return time.Now().After(lastUpdate.Add(timeout))
}

func configUpdateTime(cfg *v1alpha1.NodeNetworkConfig) metav1.Time {
	if cfg.Spec.ConfigUpdateTime != nil && cfg.Spec.ConfigUpdateTime.After(cfg.Status.LastUpdate.Time) {
		return *cfg.Spec.ConfigUpdateTime
	}
	return cfg.Status.LastUpdate
}

type nodeConfigDeployment struct {
	node          *corev1.Node
	networkConfig *v1alpha1.NodeNetworkConfig
	netplanConfig *v1alpha1.NodeNetplanConfig
}

func (crr *ConfigRevisionReconciler) getOutdatedNodes(ctx context.Context, nodes map[string]*corev1.Node, configs []v1alpha1.NodeNetworkConfig, revision *v1alpha1.NetworkConfigRevision) ([]*nodeConfigDeployment, error) {
	if revision == nil {
		return nil, nil
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config snapshot: %w", err)
	}
	crr.vrfConfig = cfg

	current := make(map[string]*v1alpha1.NodeNetworkConfig, len(configs))
	for i := range configs {
		current[configs[i].Name] = &configs[i]
	}
	nodesToDeploy := make([]*nodeConfigDeployment, 0, len(nodes))
	for _, node := range nodes {
		networkConfig, err := crr.buildNodeNetworkConfig(ctx, node, revision)
		if err != nil {
			return nil, fmt.Errorf("error preparing NodeNetworkConfig for node %s: %w", node.Name, err)
		}
		netplanConfig, err := crr.createNodeNetplanConfig(node, revision)
		if err != nil {
			return nil, fmt.Errorf("error preparing NodeNetplanConfig for node %s: %w", node.Name, err)
		}
		networkConfig.Spec.ConfigHash, err = nodeConfigHash(networkConfig.Spec, &netplanConfig.Spec)
		if err != nil {
			return nil, fmt.Errorf("error hashing configuration for node %s: %w", node.Name, err)
		}
		if cfg := current[node.Name]; cfg != nil && nodeConfigMatches(cfg, networkConfig) {
			continue
		}
		nodesToDeploy = append(nodesToDeploy, &nodeConfigDeployment{
			node: node, networkConfig: networkConfig, netplanConfig: netplanConfig,
		})
	}
	return nodesToDeploy, nil
}

func nodeConfigHash(networkSpec v1alpha1.NodeNetworkConfigSpec, netplanSpec *v1alpha1.NodeNetplanConfigSpec) (string, error) {
	networkSpec.Revision = ""
	networkSpec.ConfigHash = ""
	networkSpec.ConfigUpdateTime = nil
	data, err := json.Marshal(struct {
		Network v1alpha1.NodeNetworkConfigSpec  `json:"network"`
		Netplan *v1alpha1.NodeNetplanConfigSpec `json:"netplan"`
	}{Network: networkSpec, Netplan: netplanSpec})
	if err != nil {
		return "", fmt.Errorf("error marshaling resolved node configuration: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func nodeConfigMatches(current, desired *v1alpha1.NodeNetworkConfig) bool {
	return current.Spec.Revision == desired.Spec.Revision && current.Spec.ConfigHash == desired.Spec.ConfigHash
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

func (crr *ConfigRevisionReconciler) deployNodeConfig(ctx context.Context, deployment *nodeConfigDeployment) error {
	node := deployment.node
	newConfig := deployment.networkConfig
	currentConfig := &v1alpha1.NodeNetworkConfig{}
	if err := crr.client.Get(ctx, types.NamespacedName{Name: node.Name}, currentConfig); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("error getting NodeNetworkConfig object for node %s: %w", node.Name, err)
		}
		currentConfig = nil
	}

	if currentConfig != nil && nodeConfigMatches(currentConfig, newConfig) {
		return nil
	}

	// Deploy Netplan first so a failure cannot be hidden by the NodeNetworkConfig skip check.
	if err := crr.createOrUpdateNetplanConfig(ctx, deployment.netplanConfig); err != nil {
		return fmt.Errorf("failed to deploy NodeNetplanConfig: %w", err)
	}

	for i := 0; i < numOfDeploymentRetries; i++ {
		if err := crr.deployNodeNetworkConfig(ctx, newConfig, currentConfig, node); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && i < numOfDeploymentRetries-1 {
				continue
			}
			return fmt.Errorf("error deploying NodeNetworkConfig for node %s: %w", node.Name, err)
		}
		break
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

func (crr *ConfigRevisionReconciler) CreateNodeNetworkConfig(ctx context.Context, node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) (*v1alpha1.NodeNetworkConfig, error) {
	if err := crr.vrfConfig.ReloadConfig(); err != nil {
		return nil, fmt.Errorf("error reloading config: %w", err)
	}
	return crr.buildNodeNetworkConfig(ctx, node, revision)
}

func (crr *ConfigRevisionReconciler) buildNodeNetworkConfig(ctx context.Context, node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) (*v1alpha1.NodeNetworkConfig, error) {
	// create new config
	c := &v1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
		},
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
	if err := crr.buildNodeMirror(ctx, node, revision, c); err != nil {
		return nil, fmt.Errorf("error building node mirror config: %w", err)
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

func (crr *ConfigRevisionReconciler) createOrUpdateNetplanConfig(ctx context.Context, netplanConfig *v1alpha1.NodeNetplanConfig) error {
	nodeName := netplanConfig.Name
	currentNetplanConfig := &v1alpha1.NodeNetplanConfig{}
	if err := crr.client.Get(ctx, types.NamespacedName{Name: nodeName}, currentNetplanConfig); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("error getting NodeNetplanConfig object for node %s: %w", nodeName, err)
		}
		currentNetplanConfig = nil
	}
	if currentNetplanConfig == nil {
		if err := crr.client.Create(ctx, netplanConfig); err != nil {
			return fmt.Errorf("error creating NodeNetplanConfig for node %s: %w", nodeName, err)
		}
	} else {
		if equality.Semantic.DeepEqual(currentNetplanConfig.Spec, netplanConfig.Spec) &&
			equality.Semantic.DeepEqual(currentNetplanConfig.OwnerReferences, netplanConfig.OwnerReferences) {
			return nil
		}
		currentNetplanConfig.Spec = netplanConfig.Spec
		currentNetplanConfig.OwnerReferences = netplanConfig.OwnerReferences
		if err := crr.client.Update(ctx, currentNetplanConfig); err != nil {
			return fmt.Errorf("error updating NodeNetplanConfig for node %s: %w", nodeName, err)
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
		newConfig.Spec.ConfigUpdateTime = cfg.Spec.ConfigUpdateTime
		if cfg.Spec.Revision != newConfig.Spec.Revision {
			delete(cfg.Annotations, nodeLocalRevisionAnnotation)
		}
		if cfg.Spec.ConfigHash != newConfig.Spec.ConfigHash {
			now := metav1.Now()
			newConfig.Spec.ConfigUpdateTime = &now
			if cfg.Spec.Revision == newConfig.Spec.Revision &&
				(isNodeLocalConfig(cfg) || (cfg.Status.ConfigStatus == StatusProvisioned &&
					cfg.Spec.ConfigHash == cfg.Status.LastAppliedConfigHash)) {
				if cfg.Annotations == nil {
					cfg.Annotations = make(map[string]string)
				}
				cfg.Annotations[nodeLocalRevisionAnnotation] = newConfig.Spec.Revision
			}
		}
		// there already is config for node - update
		cfg.Spec = newConfig.Spec
		cfg.ObjectMeta.OwnerReferences = newConfig.ObjectMeta.OwnerReferences
		cfg.Name = node.Name
		if err := crr.client.Update(deploymentCtx, cfg); err != nil {
			return fmt.Errorf("error updating NodeNetworkConfig for node %s: %w", node.Name, err)
		}
	} else {
		cfg = newConfig
		now := metav1.Now()
		cfg.Spec.ConfigUpdateTime = &now
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
			// TODO(preexisting): Consider using node taint node.kubernetes.io/not-ready instead of Conditions for revision status
			if list.Items[i].Status.Conditions[j].Type == corev1.NodeReady &&
				list.Items[i].Status.Conditions[j].Status == corev1.ConditionTrue {
				nodes[list.Items[i].Name] = &list.Items[i]
				break
			}
		}
	}

	return nodes, nil
}
