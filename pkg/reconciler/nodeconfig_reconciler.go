package reconciler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
)

// NodeConfigReconciler is responsible for creating NodeConfig objects.
type NodeConfigReconciler struct {
	logger    logr.Logger
	debouncer *debounce.Debouncer
	client    client.Client
	timeout   time.Duration
}

// Reconcile starts reconciliation.
func (ncr *NodeConfigReconciler) Reconcile(ctx context.Context) {
	ncr.debouncer.Debounce(ctx)
}

// // NewNodeConfigReconciler creates new reconciler that creates NodeConfig objects.
func NewNodeConfigReconciler(clusterClient client.Client, logger logr.Logger, timeout time.Duration) (*NodeConfigReconciler, error) {
	reconciler := &NodeConfigReconciler{
		logger:  logger,
		timeout: timeout,
		client:  clusterClient,
	}

	reconciler.debouncer = debounce.NewDebouncer(reconciler.reconcileDebounced, defaultDebounceTime, logger)

	return reconciler, nil
}

func (ncr *NodeConfigReconciler) reconcileDebounced(ctx context.Context) error {
	revisions, err := listRevisions(ctx, ncr.client)
	if err != nil {
		return fmt.Errorf("error listing revisions: %w", err)
	}

	var revisionToDeploy *v1alpha1.NetworkConfigRevision
	for i := range revisions.Items {
		if revisions.Items[i].Status.IsInvalid {
			continue
		}
		revisionToDeploy = &revisions.Items[i]
		break
	}

	// there is nothing to deploy - skip
	if revisionToDeploy == nil {
		return nil
	}

	nodes, err := listNodes(ctx, ncr.client)

	if err != nil {
		return fmt.Errorf("error listing nodes: %w", err)
	}

	if err := ncr.deployNodeConfigs(ctx, nodes, revisionToDeploy); err != nil {
		return fmt.Errorf("error deploying node configurations: %w", err)
	}

	revisionToDeploy.Status.IsInvalid = false
	if err := ncr.client.Status().Update(ctx, revisionToDeploy); err != nil {
		return fmt.Errorf("error validating revision %s: %w", revisionToDeploy.Name, err)
	}

	// remove all but last known valid revision
	if err := ncr.revisionCleanup(ctx, revisionToDeploy); err != nil {
		return fmt.Errorf("error cleaning redundant revisions: %w", err)
	}

	return nil
}

func (ncr *NodeConfigReconciler) revisionCleanup(ctx context.Context, validRevision *v1alpha1.NetworkConfigRevision) error {
	revisions, err := listRevisions(ctx, ncr.client)
	if err != nil {
		return fmt.Errorf("error listing revisions: %w", err)
	}

	// remove all but last known valid revision
	for i := range revisions.Items {
		if revisions.Items[i].Spec.Revision != validRevision.Spec.Revision {
			if err := ncr.client.Delete(ctx, &revisions.Items[i]); err != nil {
				if !apierrors.IsNotFound(err) {
					return fmt.Errorf("error deleting revision %s: %w", revisions.Items[i].Name, err)
				}
			}
		}
	}

	return nil
}

func (ncr *NodeConfigReconciler) deployNodeConfigs(ctx context.Context, nodes []*corev1.Node, revision *v1alpha1.NetworkConfigRevision) error {
	for _, node := range nodes {
		newConfig, err := createConfigForNode(node, revision)
		if err != nil {
			return fmt.Errorf("error preparing config for node %s: %w", node.Name, err)
		}
		currentConfig := &v1alpha1.NodeNetworkConfig{}
		if err := ncr.client.Get(ctx, types.NamespacedName{Name: node.Name}, currentConfig); err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("error getting NodeNetworkConfig object for node %s: %w", node.Name, err)
			}
			currentConfig = nil
		}
		if currentConfig != nil && currentConfig.Spec.Revision == revision.Spec.Revision {
			// current config is the same as current revision - skip
			continue
		}
		if err := ncr.deployConfig(ctx, newConfig, currentConfig, node); err != nil {
			if errors.Is(err, InvalidConfigError) || errors.Is(err, context.DeadlineExceeded) {
				// revision results in invalid config or in context timeout - invalidate revision
				revision.Status.IsInvalid = true
				if err := ncr.client.Status().Update(ctx, revision); err != nil {
					return fmt.Errorf("error invalidating revision %s: %w", revision.Name, err)
				}
			}
			return fmt.Errorf("error deploying config for node %s: %w", node.Name, err)
		}
	}
	return nil
}

func createConfigForNode(node *corev1.Node, revision *v1alpha1.NetworkConfigRevision) (*v1alpha1.NodeNetworkConfig, error) {
	// create new config
	c := &v1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
		},
	}

	c.Spec = *revision.Spec.Config.DeepCopy()
	c.Spec.Revision = revision.Spec.Revision
	c.Name = node.Name

	err := controllerutil.SetOwnerReference(node, c, scheme.Scheme)
	if err != nil {
		return nil, fmt.Errorf("error setting owner references: %w", err)
	}

	// prepare Layer2NetworkConfigurationSpec (l2Spec) for each node.
	// Each Layer2NetworkConfigurationSpec from l2Spec has node selector,
	// which should be used to add config to proper nodes.
	// Each Layer2NetworkConfigurationSpec that don't match the node selector
	// is removed.
	for i := 0; i < len(c.Spec.Layer2); i++ {
		if c.Spec.Layer2[i].NodeSelector == nil {
			// node selector is not defined for the spec.
			// Layer2 is global - just continue
			continue
		}

		// node selector of type v1.labelSelector has to be converted
		// to labels.Selector type to be used with controller-runtime client
		selector, err := convertSelector(c.Spec.Layer2[i].NodeSelector.MatchLabels, c.Spec.Layer2[i].NodeSelector.MatchExpressions)
		if err != nil {
			return nil, fmt.Errorf("error converting selector: %w", err)
		}

		// remove currently processed Layer2NetworkConfigurationSpec if node does not match the selector
		if !selector.Matches(labels.Set(node.ObjectMeta.Labels)) {
			// TODO: is it worth to preserve order?
			c.Spec.Layer2 = append(c.Spec.Layer2[:i], c.Spec.Layer2[i+1:]...)
			i--
		}
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

func (ncr *NodeConfigReconciler) deployConfig(ctx context.Context, newConfig, currentConfig *v1alpha1.NodeNetworkConfig, node *corev1.Node) error {
	if currentConfig != nil {
		// there already is config for node - update
		currentConfig.Spec = *newConfig.Spec.DeepCopy()
		currentConfig.Status = *newConfig.Status.DeepCopy()
		currentConfig.Name = node.Name
		if err := ncr.client.Update(ctx, currentConfig); err != nil {
			return fmt.Errorf("error updating config for node %s: %w", node.Name, err)
		}
	} else {
		// there is no config for node - create one
		if err := ncr.client.Create(ctx, newConfig); err != nil {
			return fmt.Errorf("error creating config for node %s: %w", node.Name, err)
		}
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, ncr.timeout)
	defer cancel()

	// wait for agent to set status to 'provisioning'
	if err := ncr.waitForConfig(timeoutCtx, newConfig, StatusProvisioning); err != nil {
		return fmt.Errorf("error waiting for config: %w", err)
	}

	// wait for agent to set status to 'provisioned'
	if err := ncr.waitForConfig(timeoutCtx, newConfig, StatusProvisioned); err != nil {
		return fmt.Errorf("error waiting for config: %w", err)
	}

	return nil
}

func (ncr *NodeConfigReconciler) waitForConfig(ctx context.Context, config *v1alpha1.NodeNetworkConfig, expectedStatus string) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context done: %w", ctx.Err())
		default:
			if err := ncr.client.Get(ctx, client.ObjectKeyFromObject(config), config); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("error updating API boject: %w", err)
			}
			// agent set status as expected - return
			if config.Status.ConfigStatus == expectedStatus {
				return nil
			}

			// return error if status is invalid
			if config.Status.ConfigStatus == StatusInvalid {
				return fmt.Errorf("node %s config error: %w", config.Name, InvalidConfigError)
			}

			node := &corev1.Node{}
			if err := ncr.client.Get(ctx, types.NamespacedName{Name: config.Name}, node); apierrors.IsNotFound(err) {
				// node was deleted - skip
				return nil
			}
		}
	}
}

func listNodes(ctx context.Context, c client.Client) ([]*corev1.Node, error) {
	// list all nodes
	list := &corev1.NodeList{}
	if err := c.List(ctx, list); err != nil {
		return nil, fmt.Errorf("unable to list nodes: %w", err)
	}

	// discard control-plane and not-ready nodes
	nodes := []*corev1.Node{}
	for i := range list.Items {
		_, isControlPlane := list.Items[i].Labels[controlPlaneLabel]
		if !isControlPlane {
			// discard nodes that are not in ready state
			for j := range list.Items[i].Status.Conditions {
				// TODO: Should taint node.kubernetes.io/not-ready be used instead of Conditions?
				if list.Items[i].Status.Conditions[j].Type == corev1.NodeReady &&
					list.Items[i].Status.Conditions[j].Status == corev1.ConditionTrue {
					nodes = append(nodes, &list.Items[i])
					break
				}
			}
		}
	}

	return nodes, nil
}

type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string {
	return e.Message
}

func (*ConfigError) Is(target error) bool {
	_, ok := target.(*ConfigError)
	return ok
}

var InvalidConfigError = &ConfigError{Message: "invalid config"}
