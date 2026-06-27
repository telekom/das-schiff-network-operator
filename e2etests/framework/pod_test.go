package framework

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/telekom/das-schiff-network-operator/e2etests/config"
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
		Client: &mirroringCreateClient{
			Client:     clientfake.NewClientBuilder().WithScheme(scheme).Build(),
			kubeClient: kubeClient,
		},
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

	podCreates := 0
	podDeletes := 0
	for _, action := range kubeClient.Actions() {
		if action.GetResource().Resource != "pods" {
			continue
		}
		switch action.GetVerb() {
		case "create":
			podCreates++
		case "delete":
			podDeletes++
		}
	}
	if podCreates != 1 {
		t.Fatalf("pod create actions = %d, want 1", podCreates)
	}
	if podDeletes < 2 {
		t.Fatalf("pod delete actions = %d, want at least 2 initial+cleanup deletes", podDeletes)
	}
	if _, err := kubeClient.CoreV1().Pods("default").Get(context.Background(), "static-ipv6", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod still exists after cleanup: %v", err)
	}
}

type mirroringCreateClient struct {
	client.Client
	kubeClient *kubefake.Clientset
}

func (c *mirroringCreateClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}

	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	_, err := c.kubeClient.CoreV1().Pods(pod.Namespace).Create(ctx, pod.DeepCopy(), metav1.CreateOptions{})
	return err
}
