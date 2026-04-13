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

	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// BuildNamePredicates returns a predicate.Funcs that filters events to only those
// where the object name contains the current node's name (from the NODE_NAME env var).
// The NODE_NAME value is read once at predicate build time. If NODE_NAME is unset,
// all Create and Update events are rejected with a single log message.
// Delete and Generic always return false.
func BuildNamePredicates() predicate.Funcs {
	nodeName := os.Getenv(healthcheck.NodenameEnv)
	if nodeName == "" {
		log.Log.V(1).Info("NODE_NAME env not set, all events will be rejected")
	}
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			if nodeName == "" {
				return false
			}
			return strings.Contains(e.Object.GetName(), nodeName)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if nodeName == "" {
				return false
			}
			return strings.Contains(e.ObjectNew.GetName(), nodeName)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}
