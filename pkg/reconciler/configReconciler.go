package reconciler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	statusProvisioning = "provisioning"
	statusInvalid      = "invalid"
	statusProvisioned  = "provisioned"

	reconcileSleep = 5 * time.Second
)

type ConfigReconciler struct {
	client    client.Client
	logger    logr.Logger
	debouncer *debounce.Debouncer
}

type reconcileConfig struct {
	*ConfigReconciler
	logr.Logger
}

func (cr *ConfigReconciler) Reconcile(ctx context.Context) {
	cr.debouncer.Debounce(ctx)
}

func NewConfigReconciler(clusterClient client.Client, logger logr.Logger) (*ConfigReconciler, error) {
	reconciler := &ConfigReconciler{
		client: clusterClient,
		logger: logger,
	}

	reconciler.debouncer = debounce.NewDebouncer(reconciler.reconcileDebounced, defaultDebounceTime, logger)

	return reconciler, nil
}

func (cr *ConfigReconciler) reconcileDebounced(ctx context.Context) error {
	r := &reconcileConfig{
		ConfigReconciler: cr,
		Logger:           cr.logger,
	}

	// get VRFRouteConfiguration objects
	l3vnis, err := r.fetchLayer3(ctx)
	if err != nil {
		return err
	}

	// get Layer2networkConfigurationObjects objects
	l2vnis, err := r.fetchLayer2(ctx)
	if err != nil {
		return err
	}

	// get RoutingTable objects
	taas, err := r.fetchTaas(ctx)
	if err != nil {
		return err
	}

	// discard metadata from previously fetched objects
	l2Spec := []v1alpha1.Layer2NetworkConfigurationSpec{}
	for i := range l2vnis {
		l2Spec = append(l2Spec, l2vnis[i].Spec)
	}

	l3Spec := []v1alpha1.VRFRouteConfigurationSpec{}
	for i := range l3vnis {
		l3Spec = append(l3Spec, l3vnis[i].Spec)
	}

	taasSpec := []v1alpha1.RoutingTableSpec{}
	for i := range taas {
		taasSpec = append(taasSpec, taas[i].Spec)
	}

	// list all nodes in the cluster
	nodes, err := cr.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("error listing nodes: %w", err)
	}

	// list all NodeConfigs that were already deployed
	existingConfigs, err := cr.ListConfigs(ctx)
	if err != nil {
		return fmt.Errorf("error listing configs: %w", err)
	}

	// prepare map of NewConfigs (hostname is a map's key)
	// we simply add l3Spec and taasSpec to *all* configs, as
	// VRFRouteConfigurationSpec (l3Spec) and RoutingTableSpec (taasSpec)
	// are global respources (no node selctors are implemented) (Am I correct here?)
	newConfigs := map[string]*v1alpha1.NodeConfig{}
	for name := range nodes {
		var c *v1alpha1.NodeConfig
		if _, exists := existingConfigs[name]; exists {
			// config already exist - update (is this safe? Won't there be any leftovers in the config?)
			cr.logger.Info("ConfigReconciler - config exists")
			x := existingConfigs[name]
			c = &x
			c.Spec.Vrf = l3Spec
			c.Spec.RoutingTable = taasSpec
		} else {
			// config does not exist - create new
			cr.logger.Info("ConfigReconciler - new config")
			c = &v1alpha1.NodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: name,
				},
				Spec: v1alpha1.NodeConfigSpec{
					Vrf:          l3Spec,
					RoutingTable: taasSpec,
				}}
		}
		newConfigs[name] = c
	}

	cr.logger.Info("existing configs", "configs", existingConfigs)
	cr.logger.Info("new configs", "configs", newConfigs)

	// prepare Layer2NetworkConfigurationSpec (l2Spec) for each node.
	// Each Layer2NetworkConfigurationSpec from l2Spec has node selector,
	// which should be used to add config to proper nodes.
	// newL2Config map is created, which holds Layer2NetworkConfigurationSpec
	// and can be accessed by key (hostname)
	newL2Config := make(map[string][]*v1alpha1.Layer2NetworkConfigurationSpec)
	for i := range l2Spec {
		if l2Spec[i].NodeSelector != nil {
			// node selector is defined for the spec.
			cr.logger.Info("ConfigReconciler - processing node selector")

			// node selector of type v1.labelSelector has to be converted
			// to labels.Selector type to be used with controller-runtime client
			selector := labels.NewSelector()
			var reqs labels.Requirements

			for key, value := range l2Spec[i].NodeSelector.MatchLabels {
				requirement, err := labels.NewRequirement(key, selection.Equals, []string{value})
				if err != nil {
					cr.logger.Error(err, "error creating MatchLabel requirement")
					return fmt.Errorf("error creating MatchLabel requirement: %w", err)
				}
				reqs = append(reqs, *requirement)
			}

			for _, req := range l2Spec[i].NodeSelector.MatchExpressions {
				lowercaseOperator := selection.Operator(strings.ToLower(string(req.Operator)))
				requirement, err := labels.NewRequirement(req.Key, lowercaseOperator, req.Values)
				if err != nil {
					cr.logger.Error(err, "error creating MatchExpression requirement")
					return fmt.Errorf("error creating MatchExpression requirement: %w", err)
				}
				reqs = append(reqs, *requirement)
			}
			selector = selector.Add(reqs...)

			// add currently processed Layer2NetworkConfigurationSpec to each node that matches the selector
			for name := range nodes {
				if selector.Matches(labels.Set(nodes[name].ObjectMeta.Labels)) {
					cr.logger.Info("node does match nodeSelector of layer2", "node", name)
					newL2Config[name] = append(newL2Config[name], &l2Spec[i])
				}
			}
		} else {
			// selector is not defined - all nodes should have currently processed Layer2NetworkConfigurationSpec
			for name := range nodes {
				newL2Config[name] = append(newL2Config[name], &l2Spec[i])
			}
		}
	}

	cr.logger.Info("new L2 config", "map", newL2Config)

	// add Layer2NetworkConfigurationSpec saved in newL2Config to respective NodeConfigs objects
	for name := range nodes {
		newConfigs[name].Spec.Layer2 = []v1alpha1.Layer2NetworkConfigurationSpec{}
		for _, v := range newL2Config[name] {
			newConfigs[name].Spec.Layer2 = append(newConfigs[name].Spec.Layer2, *v)
		}
	}

	cr.logger.Info("ConfigReconciler - API query")
	// process new NodeConfigs one by one
	for name := range newConfigs {
		cr.logger.Info("NodeConfig", name, *newConfigs[name])
		if _, exists := existingConfigs[name]; exists {
			// config already exists - update
			cr.logger.Info("ConfigReconciler - API query - update")

			// check if new config is equal to existing config
			// if so, skip the update as nothing has to be updated
			if newConfigs[name].IsEqual(existingConfigs[name]) {
				cr.logger.Info("ConfigReconciler - configs are equal")
				continue
			}

			cr.logger.Info("ConfigReconciler - configs are not equal - provisoning will start")

			err = cr.client.Update(ctx, newConfigs[name])
			if err != nil {
				cr.logger.Error(err, "error updating NodeConfig object")
				return fmt.Errorf("error updating NodeConfig object: %w", err)
			}
		} else {
			cr.logger.Info("ConfigReconciler - API query - create")
			// config does not exist - create
			err = cr.client.Create(ctx, newConfigs[name])
			if err != nil {
				cr.logger.Error(err, "error creating NodeConfig object")
				return fmt.Errorf("error creating NodeConfig object: %w", err)
			}
		}

		// give APIServer a few seconds to process the update.
		time.Sleep(reconcileSleep)

		// get current instance from APIserver so we can update the status field
		instance := &v1alpha1.NodeConfig{}
		err := cr.client.Get(ctx, types.NamespacedName{Name: newConfigs[name].Name, Namespace: newConfigs[name].Namespace}, instance)
		if err != nil {
			cr.logger.Error(err, "error getting current instance of config")
			return fmt.Errorf("error getting current instance of config: %w", err)
		}

		// update the status with statusProvisioning
		instance.Status.ConfigStatus = statusProvisioning
		err = cr.client.Status().Update(ctx, instance)
		if err != nil {
			cr.logger.Error(err, "error creating NodeConfig status")
			return fmt.Errorf("error creating NodeConfig status: %w", err)
		}

		// wait for the node to update the status to 'provisioned' or 'invalid'
		ctxTimeout, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		errCh := make(chan error)

		go cr.WaitForConfig(ctxTimeout, instance, errCh)

		select {
		case <-ctxTimeout.Done():
			return fmt.Errorf("unable to verify NodeConfig - context cancelled: %w", ctxTimeout.Err())
		case result := <-errCh:
			if result != nil {
				return fmt.Errorf("error waiting for Node config: %w", result)
			}
		}
	}

	cr.logger.Info("ConfigReconciler - success")
	return nil
}

func (cr *ConfigReconciler) ListNodes(ctx context.Context) (map[string]corev1.Node, error) {
	// list all nodes
	list := &corev1.NodeList{}
	if err := cr.client.List(ctx, list); err != nil {
		return nil, fmt.Errorf("error listing nodes: %w", err)
	}

	// discard control-plane nodes and create map of nodes
	nodes := make(map[string]corev1.Node)
	for i := range list.Items {
		if _, exists := list.Items[i].Labels["node-role.kubernetes.io/control-plane"]; !exists {
			nodes[list.Items[i].Name] = list.Items[i]
		}
	}

	return nodes, nil
}

func (cr *ConfigReconciler) ListConfigs(ctx context.Context) (map[string]v1alpha1.NodeConfig, error) {
	// list all node configs
	list := &v1alpha1.NodeConfigList{}
	if err := cr.client.List(ctx, list); err != nil {
		return nil, fmt.Errorf("error listing NodeConfigs: %w", err)
	}

	// create map of node configs
	configs := make(map[string]v1alpha1.NodeConfig)
	for i := range list.Items {
		configs[list.Items[i].Name] = list.Items[i]
	}

	return configs, nil
}

func (cr *ConfigReconciler) WaitForConfig(ctx context.Context, instance *v1alpha1.NodeConfig, errCh chan error) {
	for {
		err := cr.client.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, instance)
		if err == nil {
			if instance.Status.ConfigStatus == statusProvisioned {
				errCh <- nil
				return
			}
			if instance.Status.ConfigStatus == statusInvalid {
				errCh <- fmt.Errorf("error creating NodeConfig - node %s reported state as invalid", instance.Name)
				return
			}
		}
		cr.logger.Info("waiting for config...")
		time.Sleep(time.Millisecond * 100)
	}
}
