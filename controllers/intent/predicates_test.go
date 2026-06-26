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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

func makeVRF(name string, gen int64, labels, annotations map[string]string) *nc.VRF {
	return &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Generation:  gen,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}

func TestIntentCRDPredicate_StatusOnlyUpdateIgnored(t *testing.T) {
	p := intentCRDPredicate()
	old := makeVRF("v1", 5, map[string]string{"a": "b"}, nil)
	updated := makeVRF("v1", 5, map[string]string{"a": "b"}, nil) // same gen/labels/annot
	if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
		t.Fatalf("expected status-only update (same generation/labels/annotations) to be filtered out")
	}
}

func TestIntentCRDPredicate_GenerationBumpAccepted(t *testing.T) {
	p := intentCRDPredicate()
	old := makeVRF("v1", 5, nil, nil)
	updated := makeVRF("v1", 6, nil, nil)
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
		t.Fatalf("expected generation bump to be accepted")
	}
}

func TestIntentCRDPredicate_LabelChangeAccepted(t *testing.T) {
	p := intentCRDPredicate()
	old := makeVRF("v1", 5, map[string]string{"a": "b"}, nil)
	updated := makeVRF("v1", 5, map[string]string{"a": "c"}, nil)
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
		t.Fatalf("expected label change to be accepted (selectors may rebind)")
	}
}

func TestIntentCRDPredicate_AnnotationChangeAccepted(t *testing.T) {
	p := intentCRDPredicate()
	old := makeVRF("v1", 5, nil, map[string]string{"k": "v1"})
	updated := makeVRF("v1", 5, nil, map[string]string{"k": "v2"})
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
		t.Fatalf("expected annotation change to be accepted")
	}
}

func TestIntentCRDPredicate_CreateDeleteGenericAlwaysAccepted(t *testing.T) {
	p := intentCRDPredicate()
	v := makeVRF("v1", 1, nil, nil)
	if !p.Create(event.CreateEvent{Object: v}) {
		t.Fatalf("expected create to be accepted")
	}
	if !p.Delete(event.DeleteEvent{Object: v}) {
		t.Fatalf("expected delete to be accepted")
	}
	if !p.Generic(event.GenericEvent{Object: v}) {
		t.Fatalf("expected generic to be accepted")
	}
}

func makeNode(name string, labels map[string]string, taints []corev1.Taint, ready corev1.ConditionStatus, heartbeat time.Time) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: corev1.NodeSpec{Taints: taints},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:              corev1.NodeReady,
				Status:            ready,
				LastHeartbeatTime: metav1.NewTime(heartbeat),
			}},
		},
	}
}

func TestNodePredicate_HeartbeatOnlyIgnored(t *testing.T) {
	p := nodePredicate()
	t0 := time.Now()
	old := makeNode("n1", nil, nil, corev1.ConditionTrue, t0)
	updated := makeNode("n1", nil, nil, corev1.ConditionTrue, t0.Add(30*time.Second))
	if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
		t.Fatalf("expected heartbeat-only update to be filtered out")
	}
}

func TestNodePredicate_LabelChangeAccepted(t *testing.T) {
	p := nodePredicate()
	old := makeNode("n1", map[string]string{"role": "worker"}, nil, corev1.ConditionTrue, time.Now())
	updated := makeNode("n1", map[string]string{"role": "edge"}, nil, corev1.ConditionTrue, time.Now())
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
		t.Fatalf("expected label change to be accepted")
	}
}

func TestNodePredicate_TaintChangeAccepted(t *testing.T) {
	p := nodePredicate()
	old := makeNode("n1", nil, nil, corev1.ConditionTrue, time.Now())
	updated := makeNode("n1", nil, []corev1.Taint{{Key: "k", Value: "v", Effect: corev1.TaintEffectNoSchedule}}, corev1.ConditionTrue, time.Now())
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
		t.Fatalf("expected taint change to be accepted")
	}
}

func TestNodePredicate_ReadyTransitionAccepted(t *testing.T) {
	p := nodePredicate()
	old := makeNode("n1", nil, nil, corev1.ConditionTrue, time.Now())
	updated := makeNode("n1", nil, nil, corev1.ConditionFalse, time.Now())
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
		t.Fatalf("expected Ready transition to be accepted")
	}
}

func TestNodePredicate_CreateDeleteGenericAlwaysAccepted(t *testing.T) {
	p := nodePredicate()
	n := makeNode("n1", nil, nil, corev1.ConditionTrue, time.Now())
	if !p.Create(event.CreateEvent{Object: n}) {
		t.Fatalf("expected create to be accepted")
	}
	if !p.Delete(event.DeleteEvent{Object: n}) {
		t.Fatalf("expected delete to be accepted")
	}
	if !p.Generic(event.GenericEvent{Object: n}) {
		t.Fatalf("expected generic to be accepted")
	}
}
