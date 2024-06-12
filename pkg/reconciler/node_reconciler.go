package reconciler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	controlPlaneLabel = "node-role.kubernetes.io/control-plane"
	nodeDebauncerTime = time.Second * 5
)

//go:generate mockgen -destination ./mock/mock_node_reconciler.go . NodeReconcilerInterface
type NodeReconcilerInterface interface {
	GetNodes() map[string]*corev1.Node
}

// NodeReconciler is responsible for watching node objects.
type NodeReconciler struct {
	client    client.Client
	logger    logr.Logger
	debouncer *debounce.Debouncer
	nodes     map[string]*corev1.Node
	Mutex     sync.RWMutex
	timeout   time.Duration

	NodeReconcilerReady chan bool
	configManagerInform chan bool
	deleteNodeInform    chan []string
}

// Reconcile starts reconciliation.
func (nr *NodeReconciler) Reconcile(ctx context.Context) {
	nr.debouncer.Debounce(ctx)
}

// NewConfigReconciler creates new reconciler that creates NodeConfig objects.
func NewNodeReconciler(clusterClient client.Client, logger logr.Logger, timeout time.Duration, cmInfo chan bool, nodeDelInfo chan []string) (*NodeReconciler, error) {
	reconciler := &NodeReconciler{
		client:              clusterClient,
		logger:              logger,
		nodes:               make(map[string]*corev1.Node),
		timeout:             timeout,
		configManagerInform: cmInfo,
		deleteNodeInform:    nodeDelInfo,
	}

	reconciler.debouncer = debounce.NewDebouncer(reconciler.reconcileDebounced, nodeDebauncerTime, logger)

	return reconciler, nil
}

func (nr *NodeReconciler) reconcileDebounced(ctx context.Context) error {
	added, deleted, err := nr.update(ctx)
	if err != nil {
		return fmt.Errorf("error updating node reconciler data: %w", err)
	}

	// inform config manager that nodes were deleted
	if len(deleted) > 0 {
		nr.logger.Info("nodes deleted - inform ConfigManager", "nodes", deleted)
		nr.deleteNodeInform <- deleted
	}

	// inform config manager that new nodes were added
	if len(added) > 0 {
		nr.logger.Info("nodes added - inform ConfigManager", "nodes", added)
		nr.configManagerInform <- true
	}

	return nil
}

func (nr *NodeReconciler) update(ctx context.Context) (added, deleted []string, err error) {
	nr.Mutex.Lock()
	defer nr.Mutex.Unlock()

	timeoutCtx, cancel := context.WithTimeout(ctx, nr.timeout)
	defer cancel()

	currentNodes, err := ListNodes(timeoutCtx, nr.client)
	if err != nil {
		return nil, nil, fmt.Errorf("error listing nodes: %w", err)
	}

	added, deleted = nr.checkNodeChanges(currentNodes)
	// save list of current nodes
	nr.nodes = currentNodes

	return added, deleted, nil
}

func ListNodes(ctx context.Context, c client.Client) (map[string]*corev1.Node, error) {
	// list all nodes
	list := &corev1.NodeList{}
	if err := c.List(ctx, list); err != nil {
		return nil, fmt.Errorf("unable to list nodes: %w", err)
	}

	// discard control-plane nodes and create map of nodes
	nodes := make(map[string]*corev1.Node)
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

func (nr *NodeReconciler) checkNodeChanges(newState map[string]*corev1.Node) (added, deleted []string) {
	added = getDifference(newState, nr.nodes)
	deleted = getDifference(nr.nodes, newState)
	return added, deleted
}

func getDifference(first, second map[string]*corev1.Node) []string {
	diff := []string{}
	for name := range first {
		if _, exists := second[name]; !exists {
			diff = append(diff, name)
		}
	}
	return diff
}

func (nr *NodeReconciler) GetNodes() map[string]*corev1.Node {
	nr.Mutex.RLock()
	defer nr.Mutex.RUnlock()
	currentNodes := make(map[string]*corev1.Node)
	for k, v := range nr.nodes {
		currentNodes[k] = v
	}
	return currentNodes
}
