// Package config holds E2E test configuration loaded from environment variables.
package config

import (
	"os"
	"time"
)

// Config holds all addressing and timeout configuration for E2E tests.
type Config struct {
	// Kubeconfig paths
	Kubeconfig         string
	Cluster2Kubeconfig string

	// Cluster nodes
	WorkerNode1 string
	WorkerNode2 string

	// VRF names
	VRFM2M string

	// Test pod addresses (VLAN 501, m2m) - used by L2 Connectivity, L3, VRF, GW tests
	Macvlan01IPv4 string
	Macvlan01IPv6 string
	Macvlan02IPv4 string
	Macvlan02IPv6 string

	// Test pod addresses (VLAN 502, m2m)
	Macvlan03IPv4 string
	Macvlan03IPv6 string

	// Test pod addresses (VLAN 503, c2m)
	Macvlan04IPv4 string
	Macvlan04IPv6 string

	// VIP Failover test addresses (VLAN 501, m2m)
	FailoverVIPv4     string
	FailoverVIPv6     string
	FailoverPod01IPv4 string
	FailoverPod01IPv6 string
	FailoverPod02IPv4 string
	FailoverPod02IPv6 string

	// Gateway addresses
	M2MGWIPv4 string
	M2MGWIPv6 string
	C2MGWIPv4 string
	C2MGWIPv6 string

	// Anycast VLAN 502 test addresses (m2m, cross-VLAN)
	AnycastV502Pod01IPv4 string
	AnycastV502Pod01IPv6 string
	AnycastV502Pod02IPv4 string
	AnycastV502Pod02IPv6 string

	// Anycast VLAN 503 test addresses (c2m)
	AnycastV503Pod01IPv4 string
	AnycastV503Pod01IPv6 string
	AnycastV503Pod02IPv4 string
	AnycastV503Pod02IPv6 string

	// Anycast MACs
	AnycastMAC        string
	AnycastMACVlan502 string

	// NAT64
	NAT64DNS string

	// Intent mode: when true, operator uses intent reconciler instead of legacy.
	IntentMode bool

	// Timeouts
	NodeReadyTimeout      time.Duration
	ComponentReadyTimeout time.Duration
	PodReadyTimeout       time.Duration
	BGPTimeout            time.Duration
}

// LoadFromEnv loads configuration, using environment variables with sensible defaults.
func LoadFromEnv() *Config {
	return &Config{
		Kubeconfig:         envOr("KUBECONFIG", "/e2etests/.kubeconfig"),
		Cluster2Kubeconfig: envOr("KUBECONFIG_CLUSTER2", "/repo/e2etests/.kubeconfig-cluster2"),

		WorkerNode1: "nwop-worker",
		WorkerNode2: "nwop-worker2",

		VRFM2M: "m2m",

		// VLAN 501 (m2m) - L2 Connectivity, L3, VRF, GW tests
		Macvlan01IPv4: "10.250.0.10",
		Macvlan01IPv6: "fd94:685b:30cf:501::10",
		Macvlan02IPv4: "10.250.0.20",
		Macvlan02IPv6: "fd94:685b:30cf:501::20",

		// VLAN 502 (m2m, cross-VLAN)
		Macvlan03IPv4: "10.250.1.30",
		Macvlan03IPv6: "fd94:685b:30cf:502::30",

		// VLAN 503 (c2m)
		Macvlan04IPv4: "10.250.30.40",
		Macvlan04IPv6: "fd94:685b:30cf:503::40",

		// VIP Failover test (VLAN 501)
		FailoverVIPv4:     "10.250.0.100",
		FailoverVIPv6:     "fd94:685b:30cf:501::100",
		FailoverPod01IPv4: "10.250.0.160",
		FailoverPod01IPv6: "fd94:685b:30cf:501::160",
		FailoverPod02IPv4: "10.250.0.170",
		FailoverPod02IPv6: "fd94:685b:30cf:501::170",

		// Gateways
		M2MGWIPv4: "10.102.0.1",
		M2MGWIPv6: "fda5:25c1:193c::1",
		C2MGWIPv4: "10.102.1.1",
		C2MGWIPv6: "fda5:25c1:193d::1",

		// Anycast MACs
		AnycastMAC:        "1a:ee:cf:2f:a7:a8",
		AnycastMACVlan502: "1a:ee:cf:2f:a7:a9",

		// Anycast VLAN 502 (m2m, cross-VLAN)
		AnycastV502Pod01IPv4: "10.250.1.40",
		AnycastV502Pod01IPv6: "fd94:685b:30cf:502::40",
		AnycastV502Pod02IPv4: "10.250.1.50",
		AnycastV502Pod02IPv6: "fd94:685b:30cf:502::50",

		// Anycast VLAN 503 (c2m)
		AnycastV503Pod01IPv4: "10.250.30.50",
		AnycastV503Pod01IPv6: "fd94:685b:30cf:503::50",
		AnycastV503Pod02IPv4: "10.250.30.60",
		AnycastV503Pod02IPv6: "fd94:685b:30cf:503::60",

		// NAT64
		NAT64DNS: "fda5:25c1:193e::1",

		// Intent mode
		IntentMode: os.Getenv("E2E_INTENT_MODE") == "true",

		// Timeouts
		NodeReadyTimeout:      5 * time.Minute,
		ComponentReadyTimeout: 5 * time.Minute,
		PodReadyTimeout:       5 * time.Minute,
		BGPTimeout:            2 * time.Minute,
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
