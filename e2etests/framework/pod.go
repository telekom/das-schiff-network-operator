package framework

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

// PodOption applies optional configuration to a test pod spec.
type PodOption func(*corev1.PodSpec)

// WithDNS overrides the pod's DNS to use the given nameservers (sets DNSPolicy=None).
func WithDNS(nameservers []string) PodOption {
	return func(spec *corev1.PodSpec) {
		spec.DNSPolicy = corev1.DNSNone
		spec.DNSConfig = &corev1.PodDNSConfig{
			Nameservers: nameservers,
		}
	}
}

// WithNetAdmin adds NET_ADMIN capability to the pod's first container.
func WithNetAdmin() PodOption {
	return func(spec *corev1.PodSpec) {
		if len(spec.Containers) > 0 {
			if spec.Containers[0].SecurityContext == nil {
				spec.Containers[0].SecurityContext = &corev1.SecurityContext{}
			}
			if spec.Containers[0].SecurityContext.Capabilities == nil {
				spec.Containers[0].SecurityContext.Capabilities = &corev1.Capabilities{}
			}
			spec.Containers[0].SecurityContext.Capabilities.Add = append(
				spec.Containers[0].SecurityContext.Capabilities.Add,
				corev1.Capability("NET_ADMIN"),
			)
		}
	}
}

// CreateTestPod creates a simple test pod with network tools.
// If a pod with the same name already exists, it is deleted first.
func (f *Framework) CreateTestPod(ctx context.Context, namespace, name, nodeName string, annotations map[string]string, opts ...PodOption) error {
	// Clean up any leftover pod from a previous run.
	_ = f.KubeClient.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	// Wait until it's actually gone (up to 60s).
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_ = Poll(waitCtx, 2*time.Second, func() (bool, error) {
		_, err := f.KubeClient.CoreV1().Pods(namespace).Get(waitCtx, name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), nil
	})
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
			Containers: []corev1.Container{
				{
					Name:    "tester",
					Image:   "busybox:1.37",
					Command: []string{"sleep", "86400"},
				},
			},
			// Don't restart on failure
			RestartPolicy: corev1.RestartPolicyNever,
			// Tolerate control-plane taint
			Tolerations: []corev1.Toleration{
				{
					Key:      "node-role.kubernetes.io/control-plane",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},
		},
	}
	for _, opt := range opts {
		opt(&pod.Spec)
	}
	return f.Client.Create(ctx, pod)
}

// CreateBirdPod creates a Bird BGP speaker pod with init container for loopback IPs.
func (f *Framework) CreateBirdPod(ctx context.Context, namespace, name, nodeName string, annotations map[string]string) error {
	_ = f.KubeClient.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_ = Poll(waitCtx, 2*time.Second, func() (bool, error) {
		_, err := f.KubeClient.CoreV1().Pods(namespace).Get(waitCtx, name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), nil
	})
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
			InitContainers: []corev1.Container{
				{
					Name:    "setup-loopback",
					Image:   "ghcr.io/srl-labs/network-multitool:v0.5.0",
					Command: []string{"/bin/sh", "-c", "ip addr add 10.250.3.1/32 dev lo && ip addr add fd75:2d70:f7f7::1/128 dev lo"},
					SecurityContext: &corev1.SecurityContext{
						Capabilities: &corev1.Capabilities{
							Add: []corev1.Capability{"NET_ADMIN"},
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "bird",
					Image: "ghcr.io/akafeng/bird:3.1.2",
					SecurityContext: &corev1.SecurityContext{
						Capabilities: &corev1.Capabilities{
							Add: []corev1.Capability{"NET_ADMIN", "NET_RAW"},
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "bird-config", MountPath: "/etc/bird"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "bird-config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "bird-config-bgpaas",
							},
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
			Tolerations: []corev1.Toleration{
				{
					Key:      "node-role.kubernetes.io/control-plane",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},
		},
	}
	return f.Client.Create(ctx, pod)
}

// WaitForPodReady waits for a pod to be in Running phase with all containers ready.
func (f *Framework) WaitForPodReady(ctx context.Context, namespace, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return Poll(ctx, 3*time.Second, func() (bool, error) {
		pod, err := f.KubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if pod.Status.Phase != corev1.PodRunning {
			return false, nil
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if !cs.Ready {
				return false, nil
			}
		}
		return true, nil
	})
}

// DeletePod force-deletes a pod and waits for it to be fully removed.
// This prevents stale macvlan interfaces from causing IP conflicts
// when multiple tests reuse the same VLAN IP addresses.
func (f *Framework) DeletePod(ctx context.Context, namespace, name string) error {
	grace := int64(0)
	err := f.KubeClient.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return Poll(waitCtx, 2*time.Second, func() (bool, error) {
		_, gerr := f.KubeClient.CoreV1().Pods(namespace).Get(waitCtx, name, metav1.GetOptions{})
		return apierrors.IsNotFound(gerr), nil
	})
}

// ExecInPod executes a command in a running pod and returns stdout, stderr.
func (f *Framework) ExecInPod(ctx context.Context, namespace, podName, containerName string, command []string) (string, string, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", f.Config.Kubeconfig)
	if err != nil {
		return "", "", err
	}

	req := f.KubeClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(restCfg, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("create SPDY executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	return stdout.String(), stderr.String(), err
}

// DockerExec runs a command inside a docker container (for containerlab nodes).
func (f *Framework) DockerExec(ctx context.Context, container string, command []string) (string, string, error) {
	args := append([]string{"exec", container}, command...)
	cmd := exec.CommandContext(ctx, "docker", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// GetPodIP returns the pod's primary IP address.
func (f *Framework) GetPodIP(ctx context.Context, namespace, name string) (string, error) {
	pod, err := f.KubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return pod.Status.PodIP, nil
}

// GetAnnotation returns a specific annotation from a pod's net-attach interface.
func (f *Framework) GetAnnotation(ctx context.Context, namespace, name, key string) (string, error) {
	pod, err := f.KubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	val, ok := pod.Annotations[key]
	if !ok {
		return "", fmt.Errorf("annotation %s not found", key)
	}
	return val, nil
}

// GetGatewayMAC retrieves the MAC address of the default gateway as seen from a pod.
func (f *Framework) GetGatewayMAC(ctx context.Context, namespace, podName, gwIP string) (string, error) {
	// Run ip neigh to get the MAC of the gateway
	stdout, _, err := f.ExecInPod(ctx, namespace, podName, "", []string{"ip", "neigh", "show", gwIP})
	if err != nil {
		return "", fmt.Errorf("ip neigh failed: %w", err)
	}
	// Parse: "10.250.0.1 dev net1 lladdr aa:bb:cc:dd:ee:ff REACHABLE"
	fields := strings.Fields(strings.TrimSpace(stdout))
	for i, field := range fields {
		if field == "lladdr" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("MAC not found in output: %s", stdout)
}
