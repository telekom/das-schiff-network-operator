package framework

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestApplySingleObjectFinalizers(t *testing.T) {
	tests := []struct {
		name       string
		manifest   string
		finalizers []string
	}{
		{
			name: "preserves existing finalizers when manifest omits finalizers",
			manifest: `apiVersion: v1
kind: ConfigMap
metadata:
  name: fixture
  namespace: default
data:
  key: updated
`,
			finalizers: []string{"cleanup.example.com"},
		},
		{
			name: "uses explicit manifest finalizers",
			manifest: `apiVersion: v1
kind: ConfigMap
metadata:
  name: fixture
  namespace: default
  finalizers:
    - manifest.example.com
data:
  key: updated
`,
			finalizers: []string{"manifest.example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			utilruntime.Must(corev1.AddToScheme(scheme))
			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "fixture",
						Namespace:  "default",
						Finalizers: []string{"cleanup.example.com"},
					},
					Data: map[string]string{"key": "old"},
				}).
				Build()

			if err := (&Framework{}).applySingleObject(context.Background(), c, []byte(tt.manifest), ""); err != nil {
				t.Fatalf("applySingleObject failed: %v", err)
			}

			got := &corev1.ConfigMap{}
			if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "fixture"}, got); err != nil {
				t.Fatalf("Get ConfigMap: %v", err)
			}
			if got.Data["key"] != "updated" {
				t.Fatalf("Expected data update, got %v", got.Data)
			}
			if !reflect.DeepEqual(got.Finalizers, tt.finalizers) {
				t.Fatalf("Expected finalizers %v, got %v", tt.finalizers, got.Finalizers)
			}
		})
	}
}
