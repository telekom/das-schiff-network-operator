/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package intent

import (
	"reflect"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/operator"
)

// intentCRDPredicate accepts only events that should trigger an intent
// re-evaluation: creates, deletes, generic, and any update where either
// the spec generation, labels, or non-ownership annotations have changed.
//
// This filters out status-only updates the controller writes back to its
// own intent CRDs (e.g. Collector.status.nodeAddresses, BGPPeering
// conditions), preventing self-trigger loops while still reacting to
// all label changes because selectors can bind on any label key.
func intentCRDPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return true
			}
			if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
				return true
			}
			if !reflect.DeepEqual(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels()) {
				return true
			}
			return !metadataEqualIgnoringKeys(e.ObjectOld.GetAnnotations(), e.ObjectNew.GetAnnotations(), ownershipAnnotationKeys)
		},
	}
}

const (
	helmReleaseNameAnnotation      = "meta.helm.sh/release-name"
	helmReleaseNamespaceAnnotation = "meta.helm.sh/release-namespace"
)

var ownershipAnnotationKeys = map[string]struct{}{
	helmReleaseNameAnnotation:      {},
	helmReleaseNamespaceAnnotation: {},
}

func metadataEqualIgnoringKeys(oldMetadata, newMetadata map[string]string, ignoredKeys map[string]struct{}) bool {
	return reflect.DeepEqual(filterMetadata(oldMetadata, ignoredKeys), filterMetadata(newMetadata, ignoredKeys))
}

func filterMetadata(metadata map[string]string, ignoredKeys map[string]struct{}) map[string]string {
	if len(metadata) == 0 {
		return nil
	}

	filtered := make(map[string]string, len(metadata))
	for key, value := range metadata {
		if _, ok := ignoredKeys[key]; ok {
			continue
		}
		filtered[key] = value
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// nodePredicate filters Node events down to changes that can plausibly
// affect intent reconciliation: creates, deletes, label changes, taint
// changes, and Ready-condition transitions. Frequent heartbeat-only
// status updates (kubelet reports, allocatable/capacity churn) are
// ignored, which prevents the intent controller from being re-triggered
// many times per minute by every Node in the cluster.
func nodePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, okOld := e.ObjectOld.(*corev1.Node)
			newNode, okNew := e.ObjectNew.(*corev1.Node)
			if !okOld || !okNew {
				// Unknown payload — fall back to triggering, safer than
				// silently dropping events.
				return true
			}
			if !reflect.DeepEqual(oldNode.Labels, newNode.Labels) {
				return true
			}
			if !reflect.DeepEqual(oldNode.Spec.Taints, newNode.Spec.Taints) {
				return true
			}
			return readyConditionChanged(oldNode, newNode)
		},
	}
}

// readyConditionChanged reports whether the NodeReady condition's Status
// transitioned between oldNode and newNode. Heartbeat-only updates that
// re-stamp LastHeartbeatTime do not flip the Status and therefore do not
// trigger reconciliation.
func readyConditionChanged(oldNode, newNode *corev1.Node) bool {
	return findReadyStatus(oldNode) != findReadyStatus(newNode)
}

func findReadyStatus(n *corev1.Node) corev1.ConditionStatus {
	for i := range n.Status.Conditions {
		if n.Status.Conditions[i].Type == corev1.NodeReady {
			return n.Status.Conditions[i].Status
		}
	}
	return corev1.ConditionUnknown
}

// nncStatusPredicate fires intent reconciliation when an NNC transitions
// out of the "provisioning" state. The intent reconciler skips spec writes
// while the on-node agent is still applying the previous spec
// (NodeNetworkConfig.Status.ConfigStatus == "provisioning"). Without this
// watch the skipped node would remain on the stale spec until the next
// unrelated intent CRD change, which causes test flakes when an L2A or
// similar object is created during a brief provisioning window.
//
// Other transitions (Create, Delete, status churn within "provisioning")
// are intentionally ignored to avoid extra reconcile work.
func nncStatusPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNNC, okOld := e.ObjectOld.(*networkv1alpha1.NodeNetworkConfig)
			newNNC, okNew := e.ObjectNew.(*networkv1alpha1.NodeNetworkConfig)
			if !okOld || !okNew {
				return false
			}
			return oldNNC.Status.ConfigStatus == operator.StatusProvisioning &&
				newNNC.Status.ConfigStatus != operator.StatusProvisioning
		},
	}
}
