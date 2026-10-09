package setup

import (
	"fmt"
)

// InstallMultus waits for the daemon to publish its CNI configuration before setup continues.
func InstallMultus(controlPlane string) error {
	version := EnvOr("MULTUS_VERSION", "v4.1.4")
	Logf("Installing Multus %s...", version)
	manifestURL := fmt.Sprintf(
		"https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/%s/deployments/multus-daemonset-thick.yml", version)
	if _, err := DockerExec(controlPlane, "kubectl", "--kubeconfig=/etc/kubernetes/admin.conf",
		"apply", "-f", manifestURL); err != nil {
		return fmt.Errorf("installing Multus: %w", err)
	}
	const patch = `{
  "spec": {
    "template": {
      "spec": {
        "containers": [{
          "name": "kube-multus",
          "resources": {
            "requests": {"memory": "512Mi"},
            "limits": {"memory": "512Mi"}
          },
          "readinessProbe": {
            "exec": {
              "command": ["/bin/sh", "-ec", "test -s /host/etc/cni/net.d/00-multus.conf && test -S /host/run/multus/multus.sock"]
            },
            "periodSeconds": 2
          }
        }]
      }
    }
  }
}`
	if _, err := DockerExec(controlPlane, "kubectl", "--kubeconfig=/etc/kubernetes/admin.conf",
		"-n", "kube-system", "patch", "daemonset", "kube-multus-ds", "--type=strategic", "-p", patch); err != nil {
		return fmt.Errorf("configuring Multus readiness: %w", err)
	}
	if _, err := DockerExec(controlPlane, "kubectl", "--kubeconfig=/etc/kubernetes/admin.conf",
		"-n", "kube-system", "rollout", "status", "daemonset/kube-multus-ds", "--timeout=120s"); err != nil {
		return fmt.Errorf("waiting for Multus: %w", err)
	}
	return nil
}
