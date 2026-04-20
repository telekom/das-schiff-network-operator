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
)

// intentCRDPredicate accepts only events that should trigger an intent
// re-evaluation: creates, deletes, generic, and any update where either
// the spec generation, labels, or annotations have changed.
//
// This filters out status-only updates the controller writes back to its
// own intent CRDs (e.g. Collector.status.nodeAddresses, BGPPeering
// conditions), preventing self-trigger loops while still reacting to
// label/annotation changes that affect selectors.
func intentCRDPredicate() predicate.Predicate {
	return predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.LabelChangedPredicate{},
		predicate.AnnotationChangedPredicate{},
	)
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
