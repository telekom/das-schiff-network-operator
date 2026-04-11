package framework

import (
	"testing"
)

func TestParseCanonicalIPv6(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
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
			name:    "IPv4-mapped IPv6 (::ffff:192.0.2.1)",
			input:   "::ffff:192.0.2.1",
			wantErr: false,
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
