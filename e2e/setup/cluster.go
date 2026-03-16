package setup

// Node holds the per-node addressing and identity for a Kubernetes node.
type Node struct {
	Name          string // container name (e.g. "nwop-control-plane")
	Role          string // "control-plane" or "worker"
	VtepIP        string
	IPv4          string
	IPv6          string
	Hostname      string // short hostname inside CRA FRR config
	BridgeMAC     string
	MgmtBridgeMAC string
}

// Cluster holds the full cluster definition.
type Cluster struct {
	Name          string
	PodSubnet     string
	ServiceSubnet string
	VIP           string
	KubeVIPImage  string
	NAT64DNS      string
	ExportCIDRv4  string
	ExportCIDRv6  string
	Nodes         []Node
}

// DefaultCluster returns the standard e2e cluster definition.
func DefaultCluster() *Cluster {
	return &Cluster{
		Name:          "nwop",
		PodSubnet:     "fd10:244::/56,10.244.0.0/16",
		ServiceSubnet: "fd10:96::/108,10.96.0.0/16",
		VIP:           "10.100.0.200",
		KubeVIPImage:  "ghcr.io/kube-vip/kube-vip:v0.9.0",
		NAT64DNS:      "fda5:25c1:193e::1",
		ExportCIDRv4:  "10.100.0.0/24",
		ExportCIDRv6:  "fdcb:f93c:3a3e::/64",
		Nodes: []Node{
			{
				Name:          "nwop-control-plane",
				Role:          "control-plane",
				VtepIP:        "10.50.0.10",
				IPv4:          "10.100.0.10",
				IPv6:          "fdcb:f93c:3a3e::10",
				Hostname:      "node-cp",
				BridgeMAC:     "02:ca:fe:00:00:10",
				MgmtBridgeMAC: "02:ca:fe:00:01:10",
			},
			{
				Name:          "nwop-worker",
				Role:          "worker",
				VtepIP:        "10.50.0.11",
				IPv4:          "10.100.0.11",
				IPv6:          "fdcb:f93c:3a3e::11",
				Hostname:      "node-w1",
				BridgeMAC:     "02:ca:fe:00:00:11",
				MgmtBridgeMAC: "02:ca:fe:00:01:11",
			},
			{
				Name:          "nwop-worker2",
				Role:          "worker",
				VtepIP:        "10.50.0.12",
				IPv4:          "10.100.0.12",
				IPv6:          "fdcb:f93c:3a3e::12",
				Hostname:      "node-w2",
				BridgeMAC:     "02:ca:fe:00:00:12",
				MgmtBridgeMAC: "02:ca:fe:00:01:12",
			},
		},
	}
}

// ControlPlane returns the first control-plane node.
func (c *Cluster) ControlPlane() *Node {
	for i := range c.Nodes {
		if c.Nodes[i].Role == "control-plane" {
			return &c.Nodes[i]
		}
	}
	return nil
}

// Workers returns all worker nodes.
func (c *Cluster) Workers() []*Node {
	var workers []*Node
	for i := range c.Nodes {
		if c.Nodes[i].Role == "worker" {
			workers = append(workers, &c.Nodes[i])
		}
	}
	return workers
}

// Cluster2 returns the second cluster definition used as a gateway cluster.
// Single-node cluster (control-plane only, taint removed) that hosts
// m2m/c2m gateway pods via macvlan Layer2NetworkConfigurations.
func Cluster2() *Cluster {
	return &Cluster{
		Name:          "nwop2",
		NAT64DNS:      "fda5:25c1:193e::1",
		PodSubnet:     "fd10:244::/56,10.244.0.0/16",
		ServiceSubnet: "fd10:96::/108,10.96.0.0/16",
		VIP:           "10.100.1.10", // no kube-vip; VIP == node IP
		ExportCIDRv4:  "10.100.1.0/24",
		ExportCIDRv6:  "fdcb:f93c:3a3e:1::/64",
		Nodes: []Node{
			{
				Name:          "nwop2-worker",
				Role:          "control-plane",
				VtepIP:        "10.50.0.20",
				IPv4:          "10.100.1.10",
				IPv6:          "fdcb:f93c:3a3e:1::10",
				Hostname:      "node2-cp",
				BridgeMAC:     "02:ca:fe:00:00:20",
				MgmtBridgeMAC: "02:ca:fe:00:01:20",
			},
		},
	}
}
