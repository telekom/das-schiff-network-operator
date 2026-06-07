package tests

import "testing"

func TestGlobalIPv6PrefixLenFromAddrOutput(t *testing.T) {
	output := `2: net1    inet6 fe80::20c:29ff:fe7c:1/64 scope link
2: net1    inet6 fd94:685b:30cf:501::170/56 scope global`

	got, err := globalIPv6PrefixLenFromAddrOutput(output)
	if err != nil {
		t.Fatalf("globalIPv6PrefixLenFromAddrOutput returned error: %v", err)
	}
	if got != "56" {
		t.Fatalf("globalIPv6PrefixLenFromAddrOutput() = %q, want %q", got, "56")
	}
}

func TestGlobalIPv6PrefixLenFromAddrOutputNoGlobalAddress(t *testing.T) {
	output := `2: net1    inet6 fe80::20c:29ff:fe7c:1/64 scope link`

	if got, err := globalIPv6PrefixLenFromAddrOutput(output); err == nil {
		t.Fatalf("globalIPv6PrefixLenFromAddrOutput() = %q, want error", got)
	}
}
