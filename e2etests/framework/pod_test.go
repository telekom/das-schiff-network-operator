package framework

import "testing"

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

func TestIPv6AddressOutputHasState(t *testing.T) {
	tests := []struct {
		name   string
		output string
		state  string
		want   bool
	}{
		{
			name: "matches tentative with variable whitespace",
			output: `4: net1@if5: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 state UP qlen 1000
    inet6 fda5:25c1:193c::1/64    scope global     tentative
       valid_lft forever preferred_lft forever`,
			state: "tentative",
			want:  true,
		},
		{
			name: "matches dadfailed token",
			output: `inet6 fda5:25c1:193c::1/64 scope global dadfailed tentative
       valid_lft forever preferred_lft forever`,
			state: "dadfailed",
			want:  true,
		},
		{
			name:   "does not match partial token",
			output: "inet6 fda5:25c1:193c::1/64 scope global nottentative",
			state:  "tentative",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ipv6AddressOutputHasState(tt.output, tt.state); got != tt.want {
				t.Fatalf("ipv6AddressOutputHasState() = %v, want %v", got, tt.want)
			}
		})
	}
}
