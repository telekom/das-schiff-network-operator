package framework

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/telekom/das-schiff-network-operator/e2etests/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestHasStaticIPv6MultusNetwork(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{
			name: "missing annotation",
			want: false,
		},
		{
			name: "plain network name",
			annotations: map[string]string{
				"k8s.v1.cni.cncf.io/networks": "macvlan-vlan501",
			},
			want: false,
		},
		{
			name: "IPv4 static IP only",
			annotations: map[string]string{
				"k8s.v1.cni.cncf.io/networks": `[{"name":"macvlan-vlan501","ips":["10.102.0.1/24"]}]`,
			},
			want: false,
		},
		{
			name: "IPv6 static prefix",
			annotations: map[string]string{
				"k8s.v1.cni.cncf.io/networks": `[{"name":"macvlan-vlan501","ips":["10.102.0.1/24","fda5:25c1:193c::1/64"]}]`,
			},
			want: true,
		},
		{
			name: "IPv6 static address without prefix",
			annotations: map[string]string{
				"k8s.v1.cni.cncf.io/networks": `[{"name":"macvlan-vlan501","ips":["fda5:25c1:193c::1"]}]`,
			},
			want: true,
		},
		{
			name: "invalid JSON",
			annotations: map[string]string{
				"k8s.v1.cni.cncf.io/networks": `[{"name":"macvlan-vlan501","ips":["fda5:25c1:193c::1/64"}`,
			},
			want: false,
		},
		{
			name: "invalid IP value",
			annotations: map[string]string{
				"k8s.v1.cni.cncf.io/networks": `[{"name":"macvlan-vlan501","ips":["not-an-ip"]}]`,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasStaticIPv6MultusNetwork(tt.annotations); got != tt.want {
				t.Fatalf("hasStaticIPv6MultusNetwork() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateTestPodDeletesStaticIPv6PodWhenReadinessFails(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}

	kubeClient := kubefake.NewClientset()
	f := &Framework{
		Config: &config.Config{
			PodReadyTimeout: time.Nanosecond,
		},
		KubeClient: kubeClient,
		Client:     clientfake.NewClientBuilder().WithScheme(scheme).Build(),
	}

	err := f.CreateTestPod(
		context.Background(),
		"default",
		"static-ipv6",
		"worker-1",
		map[string]string{
			"k8s.v1.cni.cncf.io/networks": `[{"name":"macvlan-vlan501","ips":["fda5:25c1:193c::1/64"]}]`,
		},
	)
	if err == nil {
		t.Fatal("CreateTestPod() error = nil, want readiness failure")
	}
	if !strings.Contains(err.Error(), "become ready before IPv6 DAD check") {
		t.Fatalf("CreateTestPod() error = %q, want readiness context", err)
	}

	podDeletes := 0
	for _, action := range kubeClient.Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "pods" {
			podDeletes++
		}
	}
	if podDeletes < 2 {
		t.Fatalf("pod delete actions = %d, want at least 2 initial+cleanup deletes", podDeletes)
	}
}
