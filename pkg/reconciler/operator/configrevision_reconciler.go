package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
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
}

// Reconcile starts reconciliation.
func (crr *ConfigRevisionReconciler) Reconcile(ctx context.Context) {
	crr.debouncer.Debounce(ctx)
}

// // NewNodeConfigReconciler creates new reconciler that creates NodeConfig objects.
func NewNodeConfigReconciler(clusterClient client.Client, logger logr.Logger, apiTimeout, configTimeout, preconfigTimeout time.Duration, s *runtime.Scheme, maxUpdating int) (*ConfigRevisionReconciler, error) {
	reconciler := &ConfigRevisionReconciler{
		logger:           logger,
		apiTimeout:       apiTimeout,
		configTimeout:    configTimeout,
		preconfigTimeout: preconfigTimeout,
		client:           clusterClient,
		scheme:           s,
		maxUpdating:      maxUpdating,
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
}

func (crr *ConfigRevisionReconciler) processConfigsForRevision(ctx context.Context, configs []v1alpha1.NodeNetworkConfig, revision *v1alpha1.NetworkConfigRevision) (*counters, error) {
	configs, err := crr.removeRedundantConfigs(ctx, configs)
	if err != nil {
		return nil, fmt.Errorf("failed to remove redundant configs: %w", err)
	}
	ready, ongoing, invalid := crr.getRevisionCounters(configs, revision)
	cnt := &counters{ready: ready, ongoing: ongoing, invalid: invalid}

	if invalid > 0 {
		if err := crr.invalidateRevision(ctx, revision, "NetworkConfigRevision results in invalid config"); err != nil {
			return cnt, fmt.Errorf("faild to invalidate revision %s: %w", revision.Name, err)
		}
	}

	return cnt, nil
}

func (crr *ConfigRevisionReconciler) getRevisionCounters(configs []v1alpha1.NodeNetworkConfig, revision *v1alpha1.NetworkConfigRevision) (ready, ongoing, invalid int) {
	ready = 0
	ongoing = 0
	invalid = 0
	for i := range configs {
		if configs[i].Spec.Revision == revision.Spec.Revision {
			timeout := crr.configTimeout
			switch configs[i].Status.ConfigStatus {
			case StatusProvisioned:
				// Update ready counter
				ready++
			case StatusInvalid:
				// Increase 'invalid' counter so we know that the revision results in invalid configs
				invalid++
			case "":
				// Set longer timeout if status was not yet updated
				timeout = crr.preconfigTimeout
				fallthrough
			case StatusProvisioning:
				// Update ongoing counter
				ongoing++
				if wasConfigTimeoutReached(&configs[i], timeout) {
					// If timout was reached revision is invalid (but still counts as ongoing).
					invalid++
				}
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

	// create netplan config
	if err := crr.createOrUpdateConfig(ctx, node, revision); err != nil {
		return fmt.Errorf("failed to deploy NodeNetworkConfig: %w", err)
	}

	crr.logger.Info("deployed NodeNetworkConfig", "name", newConfig.Name)

	return nil
}

func (crr *ConfigRevisionReconciler) createOrUpdateConfig(ctx context.Context, node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) error {
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

func (crr *ConfigRevisionReconciler) createConfigForNode(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) (*v1alpha1.NodeNetworkConfig, error) {
	// create new config
	c := &v1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
		},
	}

	if err := crr.vrfConfig.ReloadConfig(); err != nil {
		return nil, fmt.Errorf("error reloading config: %w", err)
	}

	vrfs := revision.Spec.Vrf
	sort.SliceStable(vrfs, func(i, j int) bool {
		return vrfs[i].Seq < vrfs[j].Seq
	})

	defaultImportMap := make(map[string]v1alpha1.VRFImport)

	c.Spec.FabricVRFs = make(map[string]v1alpha1.FabricVRF)
	c.Spec.Layer2s = make(map[string]v1alpha1.Layer2)

	for i := range vrfs {
		if _, ok := c.Spec.FabricVRFs[vrfs[i].VRF]; !ok {
			fabricVrf, err := crr.createFabricVRF(c, &vrfs[i], defaultImportMap)
			if err != nil {
				return nil, fmt.Errorf("failed to create fabric VRF definition: %w", err)
			}
			c.Spec.FabricVRFs[vrfs[i].VRF] = fabricVrf
		}
	}

	c.Spec.DefaultVRF = &v1alpha1.VRF{}
	for _, vrfImport := range defaultImportMap {
		c.Spec.DefaultVRF.VRFImports = append(c.Spec.DefaultVRF.VRFImports, vrfImport)
	}

	layer2 := revision.Spec.Layer2
	sort.SliceStable(layer2, func(i, j int) bool {
		return layer2[i].ID < layer2[j].ID
	})

	for _, l2 := range layer2 {
		if _, ok := c.Spec.Layer2s[fmt.Sprintf("%d", l2.ID)]; ok {
			return nil, fmt.Errorf("duplicate Layer2 ID found: %d", l2.ID)
		}

		nodeL2 := v1alpha1.Layer2{
			VNI:  uint32(l2.VNI), //nolint:gosec
			VLAN: uint16(l2.ID),  //nolint:gosec
			MTU:  uint16(l2.MTU), //nolint:gosec
		}
		if len(l2.AnycastGateways) > 0 {
			irb := v1alpha1.IRB{
				VRF:         l2.VRF,
				IPAddresses: l2.AnycastGateways,
				MACAddress:  l2.AnycastMac,
			}
			nodeL2.IRB = &irb
		}

		c.Spec.Layer2s[fmt.Sprintf("%d", l2.ID)] = nodeL2
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

func (crr *ConfigRevisionReconciler) createFabricVRF(c *v1alpha1.NodeNetworkConfig, vrf *v1alpha1.VRFRouteConfigurationSpec, defaultImportMap map[string]v1alpha1.VRFImport) (v1alpha1.FabricVRF, error) {
	if _, ok := c.Spec.FabricVRFs[vrf.VRF]; !ok {
		vni := uint32(0) //nolint:wastedassign
		rt := ""         //nolint:wastedassign
		if vrf.RouteTarget != nil && vrf.VNI != nil {
			vni = uint32(*vrf.VNI) //nolint:gosec
			rt = *vrf.RouteTarget
		} else if configVni, configRt, err := crr.vrfConfig.GetVNIAndRT(vrf.VRF); err == nil {
			vni = uint32(configVni) //nolint:gosec
			rt = configRt
		} else {
			return v1alpha1.FabricVRF{}, fmt.Errorf("error getting VNI and RT for VRF %s: %w", vrf.VRF, err)
		}

		fabricVrf := v1alpha1.FabricVRF{
			VRF: v1alpha1.VRF{
				VRFImports: []v1alpha1.VRFImport{
					{
						FromVRF: "cluster",
						Filter: v1alpha1.Filter{
							DefaultAction: v1alpha1.Action{
								Type: v1alpha1.Reject,
							},
						},
					},
				},
			},
			VNI:                    vni,
			EVPNImportRouteTargets: []string{},
			EVPNExportRouteTargets: []string{},
			EVPNExportFilter: &v1alpha1.Filter{
				DefaultAction: v1alpha1.Action{
					Type: v1alpha1.Reject,
				},
			},
		}
		if rt != "" {
			fabricVrf.EVPNImportRouteTargets = append(fabricVrf.EVPNImportRouteTargets, rt)
			fabricVrf.EVPNExportRouteTargets = append(fabricVrf.EVPNExportRouteTargets, rt)
		}
		c.Spec.FabricVRFs[vrf.VRF] = fabricVrf
	}

	fabricVrf := c.Spec.FabricVRFs[vrf.VRF]

	for _, aggregate := range vrf.Aggregate {
		fabricVrf.StaticRoutes = append(fabricVrf.StaticRoutes, v1alpha1.StaticRoute{
			Prefix: aggregate,
		})
	}

	processExports(vrf, &fabricVrf)

	processImports(vrf, defaultImportMap)

	return fabricVrf, nil
}

func processExports(vrf *v1alpha1.VRFRouteConfigurationSpec, fabricVrf *v1alpha1.FabricVRF) {
	sort.SliceStable(vrf.Export, func(i, j int) bool {
		return vrf.Export[i].Seq < vrf.Export[j].Seq
	})

	for _, export := range vrf.Export {
		filterItem := v1alpha1.FilterItem{
			Matcher: v1alpha1.Matcher{
				Prefix: &v1alpha1.PrefixMatcher{
					Prefix: export.CIDR,
					Ge:     export.GE,
					Le:     export.LE,
				},
			},
		}
		filterItem.Action = v1alpha1.Action{
			Type: v1alpha1.Reject,
		}
		if export.Action == "permit" {
			filterItem.Action.Type = v1alpha1.Accept
		}
		fabricVrf.EVPNExportFilter.Items = append(fabricVrf.EVPNExportFilter.Items, filterItem)

		vrfImportItem := filterItem.DeepCopy()
		if vrf.Community != nil {
			additive := true
			vrfImportItem.Action.ModifyRoute = &v1alpha1.ModifyRouteAction{
				AddCommunities:      []string{*vrf.Community},
				AdditiveCommunities: &additive,
			}
		}
		vrfImport := fabricVrf.VRFImports[0]
		vrfImport.Filter.Items = append(vrfImport.Filter.Items, *vrfImportItem)
		fabricVrf.VRFImports[0] = vrfImport
	}
}

func processImports(vrf *v1alpha1.VRFRouteConfigurationSpec, defaultImportMap map[string]v1alpha1.VRFImport) {
	sort.SliceStable(vrf.Import, func(i, j int) bool {
		return vrf.Import[i].Seq < vrf.Import[j].Seq
	})

	for _, vrfImport := range vrf.Import {
		if defaultImportMap == nil {
			defaultImportMap = make(map[string]v1alpha1.VRFImport)
		}
		if _, ok := defaultImportMap[vrf.VRF]; !ok {
			defaultImportMap[vrf.VRF] = v1alpha1.VRFImport{
				FromVRF: vrf.VRF,
				Filter: v1alpha1.Filter{
					DefaultAction: v1alpha1.Action{
						Type: v1alpha1.Reject,
					},
				},
			}
		}

		filterItem := v1alpha1.FilterItem{
			Matcher: v1alpha1.Matcher{
				Prefix: &v1alpha1.PrefixMatcher{
					Prefix: vrfImport.CIDR,
					Ge:     vrfImport.GE,
					Le:     vrfImport.LE,
				},
			},
		}
		filterItem.Action = v1alpha1.Action{
			Type: v1alpha1.Reject,
		}
		if vrfImport.Action == "permit" {
			filterItem.Action.Type = v1alpha1.Accept
		}
		vrfImport := defaultImportMap[vrf.VRF]
		vrfImport.Filter.Items = append(vrfImport.Filter.Items, filterItem)
		defaultImportMap[vrf.VRF] = vrfImport
	}
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
					VLans:   make(map[string]netplan.Device),
					Ethernets: map[string]netplan.Device{
						"hbn": {
							Raw: []byte("{}"),
						},
					},
				},
			},
		},
	}

	layer2 := revision.Spec.Layer2
	sort.SliceStable(layer2, func(i, j int) bool {
		return layer2[i].ID < layer2[j].ID
	})
	for _, l2 := range layer2 {
		vlan := map[string]interface{}{
			"id":   l2.ID,
			"link": "hbn",
			"mtu":  l2.MTU,
		}

		rawVlan, err := json.Marshal(vlan)
		if err != nil {
			return nil, fmt.Errorf("error marshaling vlan: %w", err)
		}

		c.Spec.DesiredState.Network.VLans[fmt.Sprintf("vlan.%d", l2.ID)] = netplan.Device{
			Raw: rawVlan,
		}
	}

	if err := controllerutil.SetOwnerReference(node, c, scheme.Scheme); err != nil {
		return nil, fmt.Errorf("error setting owner references (node): %w", err)
	}

	if err := controllerutil.SetOwnerReference(revision, c, crr.scheme); err != nil {
		return nil, fmt.Errorf("error setting owner references (revision): %w", err)
	}

	return c, nil
}

//nolint:unused
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
	deploymentCtx, deploymentCtxCancel := context.WithTimeout(ctx, crr.apiTimeout)
	defer deploymentCtxCancel()
	var cfg *v1alpha1.NodeNetworkConfig
	if currentConfig != nil {
		cfg = currentConfig
		// there already is config for node - update
		cfg.Spec = newConfig.Spec
		cfg.ObjectMeta.OwnerReferences = newConfig.ObjectMeta.OwnerReferences
		cfg.Name = node.Name
		cfg.Status.ConfigStatus = ""
		cfg.Status.LastUpdate = metav1.Now()
		if err := crr.client.Update(deploymentCtx, cfg); err != nil {
			return fmt.Errorf("error updating NodeNetworkConfig for node %s: %w", node.Name, err)
		}
		if err := crr.client.Status().Update(deploymentCtx, cfg); err != nil {
			return fmt.Errorf("error updating NodeNetworkConfig status for node %s: %w", node, err)
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

		if _, ok := list.Items[i].Labels["node-role.kubernetes.io/worker"]; !ok {
			delete(nodes, list.Items[i].Name)
		}
	}

	return nodes, nil
}
