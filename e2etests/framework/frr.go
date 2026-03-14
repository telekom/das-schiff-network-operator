package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// BGPSummary represents a subset of FRR's "show bgp summary json" output.
type BGPSummary struct {
	IPv4Unicast *BGPAFISummary `json:"ipv4Unicast,omitempty"`
	IPv6Unicast *BGPAFISummary `json:"ipv6Unicast,omitempty"`
	L2vpnEVPN   *BGPAFISummary `json:"l2vpnEvpn,omitempty"`
}

// BGPAFISummary holds the peers for an address family.
type BGPAFISummary struct {
	Peers map[string]BGPPeerSummary `json:"peers"`
}

// BGPPeerSummary holds the state of a single BGP peer.
type BGPPeerSummary struct {
	State         string `json:"state"`
	PfxRcd        int    `json:"pfxRcd"`
	RemoteAs      int    `json:"remoteAs"`
	MsgRcvd       int    `json:"msgRcvd"`
	MsgSent       int    `json:"msgSent"`
	NeighborCount int    `json:"neighborCount"`
}

// VtyshExec executes a vtysh command on a containerlab FRR node.
func (f *Framework) VtyshExec(ctx context.Context, container string, command string) (string, error) {
	stdout, stderr, err := f.DockerExec(ctx, container, []string{"vtysh", "-c", command})
	if err != nil {
		return "", fmt.Errorf("vtysh exec failed: stdout=%s stderr=%s err=%w", stdout, stderr, err)
	}
	return stdout, nil
}

// VtyshExecOnKindNode executes a vtysh command on a CRA-FRR instance inside a kind node.
func (f *Framework) VtyshExecOnKindNode(ctx context.Context, kindNode string, command string) (string, error) {
	stdout, stderr, err := f.DockerExec(ctx, kindNode,
		[]string{"machinectl", "shell", "cra-frr", "/usr/bin/vtysh", "-c", command})
	if err != nil {
		return "", fmt.Errorf("vtysh on %s failed: stdout=%s stderr=%s err=%w", kindNode, stdout, stderr, err)
	}
	return stdout, nil
}

// GetBGPSummary retrieves the BGP summary from a containerlab FRR node as JSON.
func (f *Framework) GetBGPSummary(ctx context.Context, container string) (*BGPSummary, error) {
	output, err := f.VtyshExec(ctx, container, "show bgp summary json")
	if err != nil {
		return nil, err
	}

	var summary BGPSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		return nil, fmt.Errorf("unmarshal BGP summary: %w (raw: %s)", err, output)
	}
	return &summary, nil
}

// GetBGPSummaryOnKindNode retrieves the BGP summary from a CRA-FRR instance.
func (f *Framework) GetBGPSummaryOnKindNode(ctx context.Context, kindNode string) (*BGPSummary, error) {
	output, err := f.VtyshExecOnKindNode(ctx, kindNode, "show bgp summary json")
	if err != nil {
		return nil, err
	}

	var summary BGPSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		return nil, fmt.Errorf("unmarshal BGP summary: %w (raw: %s)", err, output)
	}
	return &summary, nil
}

// GetBGPSummaryOnKindNodeVRF retrieves the BGP summary for a specific VRF from a CRA-FRR instance.
func (f *Framework) GetBGPSummaryOnKindNodeVRF(ctx context.Context, kindNode, vrf string) (*BGPSummary, error) {
	output, err := f.VtyshExecOnKindNode(ctx, kindNode, fmt.Sprintf("show bgp vrf %s summary json", vrf))
	if err != nil {
		return nil, err
	}

	var summary BGPSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		return nil, fmt.Errorf("unmarshal BGP VRF summary: %w (raw: %s)", err, output)
	}
	return &summary, nil
}

// CountEstablishedPeers counts the number of established BGP peers for a given AFI.
func CountEstablishedPeers(summary *BGPAFISummary) int {
	if summary == nil {
		return 0
	}
	count := 0
	for _, peer := range summary.Peers {
		if strings.EqualFold(peer.State, "Established") {
			count++
		}
	}
	return count
}

// GetEVPNRoutes retrieves EVPN routes from a container.
func (f *Framework) GetEVPNRoutes(ctx context.Context, container string) (string, error) {
	return f.VtyshExec(ctx, container, "show bgp l2vpn evpn")
}

// GetVRFRoutes retrieves routes for a specific VRF.
func (f *Framework) GetVRFRoutes(ctx context.Context, container, vrf, afi string) (string, error) {
	cmd := fmt.Sprintf("show ip route vrf %s", vrf)
	if afi == "ipv6" {
		cmd = fmt.Sprintf("show ipv6 route vrf %s", vrf)
	}
	return f.VtyshExec(ctx, container, cmd)
}
