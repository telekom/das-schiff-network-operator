package framework

import (
	"testing"
)

// TestIsIPv6Target exercises the shared isIPv6Target helper (defined in
// connectivity.go) to guard against regressions in IPv6 address detection.
// In particular, it verifies that the net.ParseIP-based approach does NOT
// produce the false positives that the former strings.Contains(target, ":")
// approach could produce (e.g. a hostname containing a colon).
func TestIsIPv6Target(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		want   bool
	}{
		// Plain IPv6 — must use ping6.
		{name: "loopback ::1", target: "::1", want: true},
		{name: "link-local fe80::1", target: "fe80::1", want: true},
		{name: "global unicast fd94:685b::10", target: "fd94:685b::10", want: true},
		{name: "fully expanded", target: "2001:0db8:0000:0000:0000:0000:0000:0001", want: true},

		// IPv4 — must use ping.
		{name: "loopback 127.0.0.1", target: "127.0.0.1", want: false},
		{name: "private 192.168.0.1", target: "192.168.0.1", want: false},
		{name: "public 8.8.8.8", target: "8.8.8.8", want: false},

		// IPv4-mapped IPv6 (::ffff:a.b.c.d) — net.ParseIP.To4() is non-nil,
		// so these are treated as IPv4 for ping purposes (same behaviour as before).
		{name: "IPv4-mapped ::ffff:192.0.2.1", target: "::ffff:192.0.2.1", want: false},

		// Edge cases that the old strings.Contains(target, ":") approach got wrong.
		{name: "invalid string is not IPv6", target: "not-an-ip", want: false},
		{name: "empty string is not IPv6", target: "", want: false},
		{name: "hostname with port colon — former false-positive", target: "example.com:8080", want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isIPv6Target(tc.target)
			if got != tc.want {
				t.Errorf("isIPv6Target(%q) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}
