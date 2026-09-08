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
	"os"
	"path/filepath"
	"testing"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// testEnvCfg is the shared envtest REST config, initialized once in TestMain.
var testEnvCfg *rest.Config

func TestMain(m *testing.M) {
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := env.Start()
	if err != nil {
		_, _ = os.Stderr.WriteString("FATAL: failed to start envtest: " + err.Error() + "\n")
		os.Exit(1)
	}
	testEnvCfg = cfg

	code := m.Run()

	if stopErr := env.Stop(); stopErr != nil {
		_, _ = os.Stderr.WriteString("WARNING: failed to stop envtest: " + stopErr.Error() + "\n")
	}

	os.Exit(code)
}

func newOperatorScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := networkv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add networkv1alpha1 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 scheme: %v", err)
	}
	return scheme
}
