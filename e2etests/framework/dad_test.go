package framework

import "testing"

func TestParseIPv6DADState(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		addr      string
		wantCIDR  string
		wantState ipv6DADState
		wantFound bool
	}{
		{
			name: "ready",
			output: `5: net1@if11: <BROADCAST,MULTICAST,UP,LOWER_UP,M-DOWN> mtu 1500 qlen 1000
    inet6 fd94:685b:30cf:501::10/64 scope global
       valid_lft forever preferred_lft forever`,
			addr:      "fd94:685b:30cf:501::10",
			wantCIDR:  "fd94:685b:30cf:501::10/64",
			wantState: ipv6DADReady,
			wantFound: true,
		},
		{
			name: "tentative",
			output: `5: net1@if11: <BROADCAST,MULTICAST,UP,LOWER_UP,M-DOWN> mtu 1500 qlen 1000
    inet6 fd94:685b:30cf:501::10/64 scope global tentative
       valid_lft forever preferred_lft forever`,
			addr:      "fd94:685b:30cf:501::10",
			wantCIDR:  "fd94:685b:30cf:501::10/64",
			wantState: ipv6DADTentative,
			wantFound: true,
		},
		{
			name: "dadfailed",
			output: `5: net1@if11: <BROADCAST,MULTICAST,UP,LOWER_UP,M-DOWN> mtu 1500 qlen 1000
    inet6 fd94:685b:30cf:501::10/64 scope global tentative dadfailed
       valid_lft forever preferred_lft forever`,
			addr:      "fd94:685b:30cf:501::10",
			wantCIDR:  "fd94:685b:30cf:501::10/64",
			wantState: ipv6DADFailed,
			wantFound: true,
		},
		{
			name: "missing",
			output: `5: net1@if11: <BROADCAST,MULTICAST,UP,LOWER_UP,M-DOWN> mtu 1500 qlen 1000
    inet6 fd94:685b:30cf:501::11/64 scope global
       valid_lft forever preferred_lft forever`,
			addr:      "fd94:685b:30cf:501::10",
			wantState: ipv6DADReady,
			wantFound: false,
		},
		{
			name: "matches expanded target to compressed output",
			output: `5: net1@if11: <BROADCAST,MULTICAST,UP,LOWER_UP,M-DOWN> mtu 1500 qlen 1000
    inet6 fd94:685b:30cf:501::10/64 scope global
       valid_lft forever preferred_lft forever`,
			addr:      "fd94:685b:30cf:501:0:0:0:10",
			wantCIDR:  "fd94:685b:30cf:501::10/64",
			wantState: ipv6DADReady,
			wantFound: true,
		},
		{
			name: "invalid target",
			output: `5: net1@if11: <BROADCAST,MULTICAST,UP,LOWER_UP,M-DOWN> mtu 1500 qlen 1000
    inet6 fd94:685b:30cf:501::10/64 scope global
       valid_lft forever preferred_lft forever`,
			addr:      "not-an-ip",
			wantState: ipv6DADReady,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCIDR, gotState, gotFound := parseIPv6DADState(tt.output, tt.addr)
			if gotFound != tt.wantFound {
				t.Fatalf("found = %v, want %v", gotFound, tt.wantFound)
			}
			if gotCIDR != tt.wantCIDR {
				t.Fatalf("cidr = %q, want %q", gotCIDR, tt.wantCIDR)
			}
			if gotState != tt.wantState {
				t.Fatalf("state = %v, want %v", gotState, tt.wantState)
			}
		})
	}
}
