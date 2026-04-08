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
	"testing"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestBuildNamePredicates_Create(t *testing.T) {
	tests := []struct {
		name     string
		nodeName string
		objName  string
		want     bool
	}{
		{
			name:     "matching node name accepts event",
			nodeName: "worker-1",
			objName:  "config-worker-1-rev1",
			want:     true,
		},
		{
			name:     "non-matching node name rejects event",
			nodeName: "worker-1",
			objName:  "config-worker-2-rev1",
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
}

func TestBuildNamePredicates_Update(t *testing.T) {
	tests := []struct {
		name     string
		nodeName string
		objName  string
		want     bool
	}{
		{
			name:     "matching node name accepts event",
			nodeName: "worker-1",
			objName:  "config-worker-1-rev1",
			want:     true,
		},
		{
			name:     "non-matching node name rejects event",
			nodeName: "worker-1",
			objName:  "config-worker-2-rev1",
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
