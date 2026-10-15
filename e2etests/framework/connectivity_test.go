package framework

import "testing"

func TestIPv6CIDRUsesReportedPrefix(t *testing.T) {
	stdout := "3: net1    inet6 2001:db8::42/56 scope global\n"
	if got, ok := ipv6CIDR(stdout, "2001:db8::42"); !ok || got != "2001:db8::42/56" {
		t.Fatalf("ipv6CIDR() = %q, %v; want 2001:db8::42/56, true", got, ok)
	}
}

func TestIPv6CIDRRejectsMissingOrMappedAddress(t *testing.T) {
	stdout := "3: net1    inet6 2001:db8::42/56 scope global\n"
	for _, address := range []string{"2001:db8::43", "::ffff:192.0.2.1"} {
		if got, ok := ipv6CIDR(stdout, address); ok || got != "" {
			t.Fatalf("ipv6CIDR(%q) = %q, %v; want empty, false", address, got, ok)
		}
	}
}
