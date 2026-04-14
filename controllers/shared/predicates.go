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
	"os"
	"strings"
	"sync"

	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// BuildNamePredicates returns a predicate.Funcs that filters events to only those
// where the object name contains the current node's name (from the NODE_NAME env var).
// Create and Update events are filtered; Delete and Generic always return false.
// When NODE_NAME is unset, the warning is logged at most once to avoid log spam.
func BuildNamePredicates() predicate.Funcs {
	var logOnce sync.Once
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			if e.Object == nil {
				log.Log.V(1).Info("Create event has nil object, rejecting event")
				return false
			}
			nodeName := os.Getenv(healthcheck.NodenameEnv)
			if nodeName == "" {
				logOnce.Do(func() {
					log.Log.V(1).Info("NODE_NAME env not set, rejecting all events until it is configured")
				})
				return false
			}
			return strings.Contains(e.Object.GetName(), nodeName)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectNew == nil {
				log.Log.V(1).Info("Update event has nil ObjectNew, rejecting event")
				return false
			}
			nodeName := os.Getenv(healthcheck.NodenameEnv)
			if nodeName == "" {
				logOnce.Do(func() {
					log.Log.V(1).Info("NODE_NAME env not set, rejecting all events until it is configured")
				})
				return false
			}
			return strings.Contains(e.ObjectNew.GetName(), nodeName)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}
