package framework

import (
	"strings"
	"testing"
)

func TestParseIPv6Target(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr string
	}{
		{
			name: "valid IPv6",
			addr: "fd94:685b:30cf:501::10",
		},
		{
			name:    "invalid target",
			addr:    "not-an-ip",
			wantErr: "invalid IPv6 address",
		},
		{
			name:    "IPv4 target",
			addr:    "192.0.2.10",
			wantErr: "is not IPv6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseIPv6Target(tt.addr)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("parseIPv6Target() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("parseIPv6Target() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := parseIPv6Target(tt.addr)
			if err != nil {
				t.Fatalf("parseIPv6Target() error = %v", err)
			}
			gotCIDR, gotState, gotFound := parseIPv6DADState(tt.output, target)
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
