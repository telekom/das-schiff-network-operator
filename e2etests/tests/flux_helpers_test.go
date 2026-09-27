package tests

import (
	"bytes"
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFluxControllersReadyRequiresPositiveReplicas(t *testing.T) {
	kube := fake.NewSimpleClientset(
		fluxDeployment("source-controller", 0, 0, 0),
		fluxDeployment("helm-controller", 1, 1, 1),
	)

	ready, err := fluxControllersReady(context.Background(), kube)
	if err != nil {
		t.Fatalf("fluxControllersReady returned error: %v", err)
	}
	if ready {
		t.Fatal("Expected zero-replica Flux Deployment not to be ready")
	}
}

func TestFluxControllersReadyAcceptsAvailableReplicas(t *testing.T) {
	kube := fake.NewSimpleClientset(
		fluxDeployment("source-controller", 1, 1, 1),
		fluxDeployment("helm-controller", 1, 1, 1),
	)

	ready, err := fluxControllersReady(context.Background(), kube)
	if err != nil {
		t.Fatalf("fluxControllersReady returned error: %v", err)
	}
	if !ready {
		t.Fatal("Expected Flux Deployments with available replicas to be ready")
	}
}

func TestGetFluxInstallManifestUsesEmbeddedFixture(t *testing.T) {
	manifest, err := getFluxInstallManifest(context.Background())
	if err != nil {
		t.Fatalf("getFluxInstallManifest returned error: %v", err)
	}
	if len(manifest) == 0 {
		t.Fatal("Expected embedded Flux install manifest to be non-empty")
	}
	if !bytes.Contains(manifest, []byte("source-controller")) {
		t.Fatal("Expected embedded manifest to contain source-controller resources")
	}
	if !bytes.Contains(manifest, []byte("helm-controller")) {
		t.Fatal("Expected embedded manifest to contain helm-controller resources")
	}
}

func TestUpsertObjectPreservesAllocatedServiceFields(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}
	clusterIP := "10.96.0.42"
	ipFamilyPolicy := corev1.IPFamilyPolicySingleStack
	internalTrafficPolicy := corev1.ServiceInternalTrafficPolicyLocal
	existing := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fixture-server",
			Namespace: "flux-system",
			Labels:    map[string]string{"old": "label"},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:             clusterIP,
			ClusterIPs:            []string{clusterIP},
			IPFamilies:            []corev1.IPFamily{corev1.IPv4Protocol},
			IPFamilyPolicy:        &ipFamilyPolicy,
			InternalTrafficPolicy: &internalTrafficPolicy,
			Selector:              map[string]string{"app": "old"},
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	c := controllerfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing).
		Build()
	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      existing.Name,
			Namespace: existing.Namespace,
			Labels:    map[string]string{"new": "label"},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "new"},
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}

	if err := upsertObject(context.Background(), c, desired); err != nil {
		t.Fatalf("upsertObject returned error for allocated Service: %v", err)
	}

	got := &corev1.Service{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(existing), got); err != nil {
		t.Fatalf("Get updated Service: %v", err)
	}
	if got.Spec.ClusterIP != clusterIP {
		t.Fatalf("ClusterIP = %q, want %q", got.Spec.ClusterIP, clusterIP)
	}
	if len(got.Spec.ClusterIPs) != 1 || got.Spec.ClusterIPs[0] != clusterIP {
		t.Fatalf("ClusterIPs = %v, want [%q]", got.Spec.ClusterIPs, clusterIP)
	}
	if len(got.Spec.IPFamilies) != 1 || got.Spec.IPFamilies[0] != corev1.IPv4Protocol {
		t.Fatalf("IPFamilies = %v, want IPv4", got.Spec.IPFamilies)
	}
	if got.Spec.IPFamilyPolicy == nil || *got.Spec.IPFamilyPolicy != ipFamilyPolicy {
		t.Fatalf("IPFamilyPolicy = %v, want %v", got.Spec.IPFamilyPolicy, ipFamilyPolicy)
	}
	if got.Spec.InternalTrafficPolicy == nil || *got.Spec.InternalTrafficPolicy != internalTrafficPolicy {
		t.Fatalf("InternalTrafficPolicy = %v, want %v", got.Spec.InternalTrafficPolicy, internalTrafficPolicy)
	}
	if got.Spec.Selector["app"] != "new" {
		t.Fatalf("Selector = %v, want updated selector", got.Spec.Selector)
	}
}

func fluxDeployment(name string, replicas, available, updated int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: fluxSystemNamespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
		Status: appsv1.DeploymentStatus{
			AvailableReplicas: available,
			UpdatedReplicas:   updated,
		},
	}
}
