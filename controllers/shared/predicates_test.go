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

package shared

import (
	"strconv"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
)

func TestBuildNamePredicates_Create(t *testing.T) {
	tests := []struct {
		name     string
		nodeName string
		objName  string
		want     bool
	}{
		{
			name:     "exact node name accepts event",
			nodeName: "worker-1",
			objName:  "worker-1",
			want:     true,
		},
		{
			name:     "prefix mismatch rejects event",
			nodeName: "worker-1",
			objName:  "worker-10",
			want:     false,
		},
		{
			name:     "empty NODE_NAME rejects event",
			nodeName: "",
			objName:  "config-worker-1-rev1",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(healthcheck.NodenameEnv, tt.nodeName)

			p := BuildNamePredicates()
			obj := &networkv1alpha1.NodeNetworkConfig{
				ObjectMeta: metav1.ObjectMeta{Name: tt.objName},
			}
			got := p.Create(event.CreateEvent{Object: obj})
			if got != tt.want {
				t.Errorf("Create() = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("nil object rejects event", func(t *testing.T) {
		t.Setenv(healthcheck.NodenameEnv, "worker-1")

		p := BuildNamePredicates()
		if got := p.Create(event.CreateEvent{}); got {
			t.Errorf("Create() = true, want false")
		}
	})
}

func TestBuildNamePredicates_Update(t *testing.T) {
	tests := []struct {
		name     string
		nodeName string
		objName  string
		want     bool
	}{
		{
			name:     "exact node name accepts event",
			nodeName: "worker-1",
			objName:  "worker-1",
			want:     true,
		},
		{
			name:     "prefix mismatch rejects event",
			nodeName: "worker-1",
			objName:  "worker-10",
			want:     false,
		},
		{
			name:     "empty NODE_NAME rejects event",
			nodeName: "",
			objName:  "config-worker-1-rev1",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(healthcheck.NodenameEnv, tt.nodeName)

			p := BuildNamePredicates()
			obj := &networkv1alpha1.NodeNetworkConfig{
				ObjectMeta: metav1.ObjectMeta{Name: tt.objName},
			}
			got := p.Update(event.UpdateEvent{ObjectNew: obj})
			if got != tt.want {
				t.Errorf("Update() = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("nil new object rejects event", func(t *testing.T) {
		t.Setenv(healthcheck.NodenameEnv, "worker-1")

		p := BuildNamePredicates()
		if got := p.Update(event.UpdateEvent{}); got {
			t.Errorf("Update() = true, want false")
		}
	})
}

func TestBuildNamePredicates_DeleteAndGeneric(t *testing.T) {
	t.Setenv(healthcheck.NodenameEnv, "worker-1")

	p := BuildNamePredicates()
	obj := &networkv1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "config-worker-1-rev1"},
	}

	if got := p.Delete(event.DeleteEvent{Object: obj}); got {
		t.Errorf("Delete() = true, want false")
	}
	if got := p.Generic(event.GenericEvent{Object: obj}); got {
		t.Errorf("Generic() = true, want false")
	}
}

func TestBuildConfigPredicates_RecoveryStatusUpdates(t *testing.T) {
	t.Setenv(healthcheck.NodenameEnv, "worker-1")
	p := BuildConfigPredicates()
	config := &networkv1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-1", UID: "current-config", Generation: 1, ResourceVersion: "1"},
		Spec:       networkv1alpha1.NodeNetworkConfigSpec{Revision: "revision-one"},
	}
	if !p.Create(event.CreateEvent{Object: config}) {
		t.Fatal("initial config must trigger reconciliation")
	}
	for i, status := range []string{"provisioning", "invalid", "provisioning", "invalid", "provisioning", "invalid"} {
		previous := config.DeepCopy()
		config.Status.ConfigStatus = status
		config.ResourceVersion = strconv.Itoa(i + 2)
		e := event.UpdateEvent{ObjectOld: previous, ObjectNew: config}
		if p.Update(e) {
			t.Errorf("status-only update to %q must not enqueue an immediate retry", status)
		}
	}
	replacement := config.DeepCopy()
	replacement.UID = "replacement-config"
	if !p.Update(event.UpdateEvent{ObjectOld: config, ObjectNew: replacement}) {
		t.Error("a recreated config observed through an informer relist must trigger reconciliation even with the same generation")
	}
	next := config.DeepCopy()
	next.Generation++
	next.Spec.Revision = "revision-two"
	if !p.Update(event.UpdateEvent{ObjectOld: config, ObjectNew: next}) {
		t.Error("a new desired config must still trigger reconciliation")
	}
	next.Name = "worker-2"
	if p.Update(event.UpdateEvent{ObjectOld: config, ObjectNew: next}) {
		t.Error("another node's config must remain filtered")
	}
	if p.Create(event.CreateEvent{Object: next}) {
		t.Error("another node's config creation must remain filtered")
	}
	if p.Delete(event.DeleteEvent{Object: config}) || p.Generic(event.GenericEvent{Object: config}) {
		t.Error("delete and generic events must remain filtered")
	}
}
