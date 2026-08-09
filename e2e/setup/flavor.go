package setup

import (
	"os"
	"path/filepath"
	"strings"
)

// CRA flavor selection for the E2E lab. The default is the cra-frr flavor; set
// E2E_CRA_FLAVOR=grout to run the same intent suite against the cra-grout
// (grout DPDK fast path) flavor instead. The flavor changes which CRA image is
// baked into the kind-node image, which agent image/DaemonSet is deployed, and
// which operator kustomize overlay selects the agent.
const (
	flavorFRR   = "frr"
	flavorGrout = "grout"

	craFlavorEnv = "E2E_CRA_FLAVOR"
)

// craFlavor returns the configured CRA flavor ("frr" default, or "grout").
func craFlavor() string {
	switch os.Getenv(craFlavorEnv) {
	case flavorGrout:
		return flavorGrout
	default:
		return flavorFRR
	}
}

// isGrout reports whether the grout flavor is selected.
func isGrout() bool { return craFlavor() == flavorGrout }

// agentImage returns the node agent image ref for the selected flavor.
func agentImage(imgBase string) string {
	if isGrout() {
		return imgBase + "/das-schiff-nwop-agent-cra-grout:latest"
	}
	return imgBase + "/das-schiff-nwop-agent-cra-frr:latest"
}

// agentDockerfile returns the agent Dockerfile for the selected flavor.
func agentDockerfile() string {
	if isGrout() {
		return "das-schiff-nwop-agent-cra-grout.Dockerfile"
	}
	return "das-schiff-nwop-agent-cra-frr.Dockerfile"
}

// agentDaemonSet returns the agent DaemonSet name for the selected flavor (used
// to wait for the agent to become ready).
func agentDaemonSet() string {
	if isGrout() {
		return "network-operator-agent-cra-grout"
	}
	return "network-operator-agent-cra-frr"
}

// operatorOverlay returns the in-container operator kustomize overlay path for
// the selected flavor. The grout overlay swaps the agent-cra-frr component for
// agent-cra-grout so the grout agent DaemonSet is deployed.
func operatorOverlay() string {
	if isGrout() {
		return "/repo/e2e/operator-grout"
	}
	return "/repo/e2e/operator"
}

// craImageName returns the CRA container image ref for the selected flavor.
func craImageName() string {
	if isGrout() {
		return "das-schiff-cra-grout:latest"
	}
	return "das-schiff-cra-frr:latest"
}

// craDockerfile returns the CRA container Dockerfile for the selected flavor.
func craDockerfile() string {
	if isGrout() {
		return "das-schiff-cra-grout.Dockerfile"
	}
	return "das-schiff-cra-frr.Dockerfile"
}

// kindNodeContext returns the kind-node image build context and the CRA tar path
// (inside that context) into which the CRA image is saved before the node build.
func kindNodeContext(repoRoot string) (ctxDir, craTar string) {
	if isGrout() {
		ctxDir = filepath.Join(repoRoot, "e2e", "images", "kind-node-grout")
		return ctxDir, filepath.Join(ctxDir, "cra-grout.tar")
	}
	ctxDir = filepath.Join(repoRoot, "e2e", "images", "kind-node")
	return ctxDir, filepath.Join(ctxDir, "cra-frr.tar")
}

// kindNodeDockerfile returns the kind-node Dockerfile for the selected flavor.
// The grout Dockerfile lives in its own context but is built from the repo root
// so it can COPY the shared kind-node host tooling.
func kindNodeDockerfile(repoRoot string) string {
	if isGrout() {
		return filepath.Join(repoRoot, "e2e", "images", "kind-node-grout", "Dockerfile")
	}
	return filepath.Join(repoRoot, "e2e", "images", "kind-node", "Dockerfile")
}

// kindNodeBuildContext returns the docker build context for the kind-node image.
// The grout node Dockerfile builds from the repo root (it COPYs the shared
// kind-node host tooling under e2e/images/kind-node); the cra-frr node builds
// from its own directory.
func kindNodeBuildContext(repoRoot, kindNodeCtx string) string {
	if isGrout() {
		return repoRoot
	}
	return kindNodeCtx
}

// logFlavor prints the selected flavor once at the start of the image phase.
func logFlavor() {
	Logf("CRA flavor: %s (set %s=grout for the grout DPDK fast path)", craFlavor(), craFlavorEnv)
}

// craHbnAddr returns the CRA-side address of the node<->CRA trunk, which is the
// node's default gateway and the BGP peer kube-vip advertises the API VIP to.
// The grout flavour cannot reuse the FRR transfer net: there fd00:7:caa5::/127
// is the `cra-mgmt` control-plane veth, so its data trunk has its own prefix.
func craHbnAddr() string {
	if isGrout() {
		return groutHbnCRAv6Addr
	}
	return "fd00:7:caa5::"
}

// nodeHbnAddr returns the node-side address of the node<->CRA trunk, which
// kube-vip uses as its BGP source address.
func nodeHbnAddr() string {
	if isGrout() {
		return groutHbnNodeV6Addr
	}
	return "fd00:7:caa5::1"
}

// renderKubeVIP substitutes the flavour-dependent trunk addresses into a
// kube-vip manifest. Left hardcoded, kube-vip on the grout flavour peers with an
// address that only exists on the FRR flavour, never establishes, and the API
// VIP is never advertised into the fabric -- which surfaces much later as
// `kubeadm join` timing out against the control-plane endpoint.
func renderKubeVIP(manifest string) string {
	manifest = strings.ReplaceAll(manifest, "__CRA_HBN_ADDR__", craHbnAddr())
	return strings.ReplaceAll(manifest, "__NODE_HBN_ADDR__", nodeHbnAddr())
}

// workloadTransport is the CNI transport a routed attachment must request on
// the selected flavour. grout's fast path cannot adopt a netdev created outside
// it (no af_packet/af_xdp/memif PMD in the edge image), so it creates the
// CRA-side tap itself and hands the name back to the CNI.
func workloadTransport() string {
	if isGrout() {
		return "grouttap"
	}
	return "veth"
}

// renderWorkloadNAD substitutes the flavour-dependent transport into a
// NetworkAttachmentDefinition that uses the routed workload CNI.
func renderWorkloadNAD(manifest string) string {
	return strings.ReplaceAll(manifest, "__TRANSPORT__", workloadTransport())
}
