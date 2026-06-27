package framework

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"
)

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
			name: "prefix collision",
			output: `5: net1@if11: <BROADCAST,MULTICAST,UP,LOWER_UP,M-DOWN> mtu 1500 qlen 1000
    inet6 fd94:685b:30cf:501::100/64 scope global
       valid_lft forever preferred_lft forever`,
			addr:      "fd94:685b:30cf:501::10",
			wantState: ipv6DADReady,
			wantFound: false,
		},
		{
			name: "malformed output prefix",
			output: `5: net1@if11: <BROADCAST,MULTICAST,UP,LOWER_UP,M-DOWN> mtu 1500 qlen 1000
    inet6 fd94:685b:30cf:501::10/not-a-prefix scope global
       valid_lft forever preferred_lft forever`,
			addr:      "fd94:685b:30cf:501::10",
			wantState: ipv6DADReady,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tt.addr)
			gotCIDR, gotState, gotFound := parseIPv6DADState(tt.output, addr)
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

func TestWaitForIPv6DADCompleteRejectsInvalidAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr string
	}{
		{
			name:    "malformed",
			addr:    "not-an-ip",
			wantErr: "parse IPv6 address",
		},
		{
			name:    "ipv4",
			addr:    "192.0.2.10",
			wantErr: "is not IPv6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&Framework{}).WaitForIPv6DADComplete(context.Background(), "default", "pod", tt.addr, "net1", time.Second)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
