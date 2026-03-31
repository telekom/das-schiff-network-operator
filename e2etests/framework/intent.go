package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	operatorNamespace  = "kube-system"
	operatorDeployment = "network-operator-operator"
	intentRBACName     = "network-operator-intent"
	intentAPIGroup     = "network-connector.sylvaproject.org"
	nncGVR             = "nodenetworkconfigs"
	nncGroup           = "network.t-caas.telekom.com"
	nncVersion         = "v1alpha1"
)

// EnableIntentReconciler patches the operator deployment to enable the intent
// reconciler and creates the necessary RBAC for intent CRDs. It then waits
// for the operator to restart.
func (f *Framework) EnableIntentReconciler(ctx context.Context) error {
	// 1. Create RBAC for intent CRD API group.
	if err := f.ensureIntentRBAC(ctx); err != nil {
		return fmt.Errorf("create intent RBAC: %w", err)
	}

	// 2. Delete legacy VRFRouteConfigurations so intent reconciler starts clean.
	if err := f.deleteLegacyConfigs(ctx); err != nil {
		return fmt.Errorf("delete legacy configs: %w", err)
	}

	// 3. Patch operator deployment to add --enable-intent-reconciler arg.
	if err := f.patchOperatorForIntent(ctx); err != nil {
		return fmt.Errorf("patch operator deployment: %w", err)
	}

	// 4. Wait for operator pod to restart with new args.
	if err := f.waitForOperatorReady(ctx, 120*time.Second); err != nil {
		return fmt.Errorf("wait for operator restart: %w", err)
	}

	return nil
}

// ensureIntentRBAC creates the ClusterRole + ClusterRoleBinding for intent CRDs.
func (f *Framework) ensureIntentRBAC(ctx context.Context) error {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: intentRBACName},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{intentAPIGroup},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{intentAPIGroup},
				Resources: []string{"*/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
		},
	}

	if err := f.Client.Create(ctx, cr); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: intentRBACName},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     intentRBACName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "network-operator-controller-manager",
				Namespace: operatorNamespace,
			},
		},
	}

	if err := f.Client.Create(ctx, crb); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	return nil
}

// deleteLegacyConfigs removes VRFRouteConfigurations and Layer2NetworkConfigurations.
func (f *Framework) deleteLegacyConfigs(ctx context.Context) error {
	for _, resource := range []string{"vrfrouteconfigurations", "layer2networkconfigurations"} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "network.t-caas.telekom.com",
			Version: "v1alpha1",
			Kind:    resource,
		})
		if err := f.Client.List(ctx, list); err != nil {
			// GVK might not exist, skip.
			continue
		}
		for i := range list.Items {
			if err := f.Client.Delete(ctx, &list.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete %s/%s: %w", resource, list.Items[i].GetName(), err)
			}
		}
	}
	return nil
}

// patchOperatorForIntent adds --enable-intent-reconciler to the operator container args.
func (f *Framework) patchOperatorForIntent(ctx context.Context) error {
	deploy := &appsv1.Deployment{}
	if err := f.Client.Get(ctx, types.NamespacedName{
		Name:      operatorDeployment,
		Namespace: operatorNamespace,
	}, deploy); err != nil {
		return err
	}

	// Check if already patched.
	for _, arg := range deploy.Spec.Template.Spec.Containers[0].Args {
		if arg == "--enable-intent-reconciler" {
			return nil // already enabled
		}
	}

	// Use JSON patch to append the arg.
	patch := []map[string]interface{}{
		{
			"op":    "add",
			"path":  "/spec/template/spec/containers/0/args/-",
			"value": "--enable-intent-reconciler",
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	return f.Client.Patch(ctx, deploy, client.RawPatch(types.JSONPatchType, patchBytes))
}

// waitForOperatorReady waits for the operator deployment to be available after restart.
func (f *Framework) waitForOperatorReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Wait briefly for the rollout to start.
	time.Sleep(5 * time.Second)

	return Poll(ctx, 5*time.Second, func() (bool, error) {
		deploy, err := f.KubeClient.AppsV1().Deployments(operatorNamespace).Get(ctx, operatorDeployment, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		// Check rollout complete: updatedReplicas == replicas && availableReplicas == replicas.
		return deploy.Status.UpdatedReplicas == *deploy.Spec.Replicas &&
			deploy.Status.AvailableReplicas == *deploy.Spec.Replicas &&
			deploy.Status.UnavailableReplicas == 0, nil
	})
}

// WaitForIntentNNCs waits for the intent reconciler to produce NodeNetworkConfig
// resources with the intent-managed label for all worker nodes.
func (f *Framework) WaitForIntentNNCs(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return Poll(ctx, 5*time.Second, func() (bool, error) {
		nodes, err := f.KubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil
		}

		for i := range nodes.Items {
			node := &nodes.Items[i]
			nnc := &unstructured.Unstructured{}
			nnc.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   nncGroup,
				Version: nncVersion,
				Kind:    "NodeNetworkConfig",
			})
			if err := f.Client.Get(ctx, types.NamespacedName{Name: node.Name}, nnc); err != nil {
				return false, nil
			}
			labels := nnc.GetLabels()
			if labels == nil || labels["network-connector.sylvaproject.org/managed-by"] != "intent" {
				return false, nil
			}
		}
		return true, nil
	})
}

// IsIntentMode returns true if the framework is configured for intent mode.
func (f *Framework) IsIntentMode() bool {
	return f.Config.IntentMode
}
