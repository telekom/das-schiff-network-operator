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

// Package testutil provides shared test helpers for agent controller tests.
package testutil

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// ObjectWithName is a factory that creates a client.Object with the given name.
type ObjectWithName func(name string) client.Object

// RunNamePredicateCreateTests exercises the Create predicate for the given predicate.Funcs.
// newObj must return a client.Object whose metadata name is set to the provided string.
func RunNamePredicateCreateTests(t *testing.T, pred predicate.Funcs, newObj ObjectWithName) {
	t.Helper()

	tests := []struct {
		objectName string
		want       bool
	}{
		{"worker-node-01", true},
		{"prefix-worker-node-01-suffix", true},
		{"other-node", false},
	}

	for _, tt := range tests {
		t.Run(tt.objectName, func(t *testing.T) {
			got := pred.Create(event.CreateEvent{Object: newObj(tt.objectName)})
			if got != tt.want {
				t.Errorf("Create predicate(%q) = %v, want %v", tt.objectName, got, tt.want)
			}
		})
	}
}

// RunNamePredicateUpdateTests exercises the Update predicate for the given predicate.Funcs.
func RunNamePredicateUpdateTests(t *testing.T, pred predicate.Funcs, newObj ObjectWithName) {
	t.Helper()

	tests := []struct {
		newName string
		want    bool
	}{
		{"worker-node-01", true},
		{"other-node", false},
	}

	for _, tt := range tests {
		t.Run(tt.newName, func(t *testing.T) {
			got := pred.Update(event.UpdateEvent{ObjectNew: newObj(tt.newName)})
			if got != tt.want {
				t.Errorf("Update predicate(%q) = %v, want %v", tt.newName, got, tt.want)
			}
		})
	}
}

// RunDeleteAndGenericAlwaysFalse asserts that Delete and Generic predicates always return false.
func RunDeleteAndGenericAlwaysFalse(t *testing.T, pred predicate.Funcs) {
	t.Helper()

	if pred.Delete(event.DeleteEvent{}) {
		t.Error("Delete predicate should always return false")
	}
	if pred.Generic(event.GenericEvent{}) {
		t.Error("Generic predicate should always return false")
	}
}

// RunEmptyNodeNameTest asserts that the predicate returns false when NODE_NAME env is empty.
// Both Create and Update events are checked because predicates are evaluated on both paths.
func RunEmptyNodeNameTest(t *testing.T, pred predicate.Funcs, newObj ObjectWithName) {
	t.Helper()

	obj := newObj("any-object-name")

	// Create event must return false when no NODE_NAME is set
	if pred.Create(event.CreateEvent{Object: obj}) {
		t.Error("Create: expected false when NODE_NAME env is empty: predicate must not match all objects")
	}

	// Update event must also return false when no NODE_NAME is set
	if pred.Update(event.UpdateEvent{ObjectNew: obj, ObjectOld: obj}) {
		t.Error("Update: expected false when NODE_NAME env is empty: predicate must not match all objects")
	}
}

// RunNilObjectSafetyTest asserts that the predicate returns false (and does not panic)
// when a nil object is delivered in Create or Update events.
func RunNilObjectSafetyTest(t *testing.T, pred predicate.Funcs) {
	t.Helper()

	// Create event with nil Object must not panic and must return false.
	if got := pred.Create(event.CreateEvent{Object: nil}); got {
		t.Error("Create: expected false when Object is nil")
	}

	// Update event with nil ObjectNew must not panic and must return false.
	if got := pred.Update(event.UpdateEvent{ObjectNew: nil}); got {
		t.Error("Update: expected false when ObjectNew is nil")
	}
}

// NewObjectMeta returns an ObjectMeta with the given name, for use with ObjectWithName factories.
func NewObjectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name}
}
