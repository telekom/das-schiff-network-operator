package framework

import (
	"strings"
	"testing"
)

func TestParseCanonicalIPv6(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		input           string
		wantErr         bool
		wantErrContains string
	}{
		{
			name:    "valid compressed IPv6",
			input:   "fd94:685b:30cf:501::10",
			wantErr: false,
		},
		{
			name:    "valid fully-expanded IPv6",
			input:   "2001:0db8:0000:0000:0000:0000:0000:0001",
			wantErr: false,
		},
		{
			name:    "valid loopback",
			input:   "::1",
			wantErr: false,
		},
		{
			name:    "valid link-local",
			input:   "fe80::1",
			wantErr: false,
		},
		{
			name:            "IPv4-mapped IPv6 is rejected",
			input:           "::ffff:192.0.2.1",
			wantErr:         true,
			wantErrContains: "IPv4-mapped IPv6",
		},
		{
			name:    "plain IPv4 is rejected",
			input:   "192.168.1.1",
			wantErr: true,
		},
		{
			name:    "empty string is rejected",
			input:   "",
			wantErr: true,
		},
		{
			name:    "garbage string is rejected",
			input:   "not-an-ip",
			wantErr: true,
		},
		{
			name:    "IPv6 with prefix length is rejected",
			input:   "fd94:685b:30cf:501::10/64",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, err := parseCanonicalIPv6(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseCanonicalIPv6(%q) = %v, want error", tc.input, addr)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("parseCanonicalIPv6(%q) error = %q, want substring %q", tc.input, err, tc.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Errorf("parseCanonicalIPv6(%q) returned unexpected error: %v", tc.input, err)
				return
			}
			if !addr.IsValid() {
				t.Errorf("parseCanonicalIPv6(%q) returned invalid addr", tc.input)
			}
		})
	}
}

func TestParseCanonicalIPv6_CanonicalEquality(t *testing.T) {
	t.Parallel()

	// Compressed and expanded representations of the same address must be equal.
	compressed := "fd94:685b:30cf:501::10"
	expanded := "fd94:685b:30cf:0501:0000:0000:0000:0010"

	a, err := parseCanonicalIPv6(compressed)
	if err != nil {
		t.Fatalf("parseCanonicalIPv6(%q): %v", compressed, err)
	}
	b, err := parseCanonicalIPv6(expanded)
	if err != nil {
		t.Fatalf("parseCanonicalIPv6(%q): %v", expanded, err)
	}
	if a != b {
		t.Errorf("canonical forms differ: %v != %v", a, b)
	}
}

func TestFindIPv6AddressWithPrefix(t *testing.T) {
	t.Parallel()

	output := `2: net1    inet6 fe80::20c:29ff:fe7c:1/64 scope link
2: net1    inet6 fd94:685b:30cf:501::10/80 scope global tentative
2: net1    inet6 2001:db8::1/96 scope global`

	got, err := findIPv6AddressWithPrefix(output, "fd94:685b:30cf:501::10")
	if err != nil {
		t.Fatalf("findIPv6AddressWithPrefix returned error: %v", err)
	}
	if got != "fd94:685b:30cf:501::10/80" {
		t.Errorf("findIPv6AddressWithPrefix returned %q, want %q", got, "fd94:685b:30cf:501::10/80")
	}
}

func TestFindIPv6AddressWithPrefix_CanonicalMatch(t *testing.T) {
	t.Parallel()

	output := `2: net1    inet6 fd94:685b:30cf:501::10/112 scope global`

	got, err := findIPv6AddressWithPrefix(output, "fd94:685b:30cf:0501:0000:0000:0000:0010")
	if err != nil {
		t.Fatalf("findIPv6AddressWithPrefix returned error: %v", err)
	}
	if got != "fd94:685b:30cf:501::10/112" {
		t.Errorf("findIPv6AddressWithPrefix returned %q, want %q", got, "fd94:685b:30cf:501::10/112")
	}
}

func TestFindIPv6AddressWithPrefix_NotFound(t *testing.T) {
	t.Parallel()

	_, err := findIPv6AddressWithPrefix(`2: net1 inet6 2001:db8::1/64 scope global`, "fd94:685b:30cf:501::10")
	if err == nil {
		t.Fatal("findIPv6AddressWithPrefix returned nil error, want not found error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("findIPv6AddressWithPrefix error = %q, want substring %q", err, "not found")
	}
}
