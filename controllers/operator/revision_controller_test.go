//go:build integration

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

package operator

import (
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
)

func TestRevisionReconciler_SetupWithManager(t *testing.T) {
	mgr, err := ctrl.NewManager(testEnvCfg, ctrl.Options{
		Scheme: newOperatorScheme(t),
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	r := &RevisionReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}

	if err := r.SetupWithManager(mgr); err != nil {
		t.Errorf("SetupWithManager() returned unexpected error: %v", err)
	}
}
