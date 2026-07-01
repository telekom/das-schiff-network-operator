package tests

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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
