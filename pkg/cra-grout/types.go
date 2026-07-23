// Package cra provides the cra-grout flavor: an FRR control plane paired with
// the grout (github.com/DPDK/grout) DPDK graph-router fast path. It renders the
// NodeNetworkConfig into (a) an FRR configuration (reusing the cra-frr template)
// and (b) a grcli batch script, then applies both to a grout-cra sidecar running
// inside the grout container in the CRA network namespace.
package cra

// Configuration is the payload posted to the grout-cra sidecar's
// /grout/configuration endpoint. It mirrors pkg/cra-frr.Configuration but
// carries a grcli batch (the grout fast-path desired state) instead of a
// netlink configuration.
type Configuration struct {
	// FRRConfiguration is the rendered frr.conf written by the sidecar and
	// applied via `reload`. grout ships a patched FRR with the dplane_grout.so
	// zebra plugin, so FRR programs grout's FIB for BGP/EVPN routes.
	FRRConfiguration string `json:"frr"`
	// GrcliBatch is the newline-separated grcli command batch describing the
	// desired grout state (VRFs, ports, addresses, routes, bridges, VXLANs).
	// The sidecar applies it via `grcli -ef`.
	GrcliBatch string `json:"grcli"`
}
