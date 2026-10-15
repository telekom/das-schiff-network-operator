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

func TestGlobalIPv6PrefixLenFromAddrOutputRequiresGlobalScope(t *testing.T) {
	output := `2: net1    inet6 2001:db8::1/64 scope host`

	if got, err := globalIPv6PrefixLenFromAddrOutput(output); err == nil {
		t.Fatalf("globalIPv6PrefixLenFromAddrOutput() = %q, want error", got)
	}
}

func TestVIPWithPrefixKeepsPodSpecificPrefixes(t *testing.T) {
	vip := "2001:db8::100"
	if got := vipWithPrefix(vip, "64"); got != "2001:db8::100/64" {
		t.Fatalf("destination VIP = %q", got)
	}
	if got := vipWithPrefix(vip, "56"); got != "2001:db8::100/56" {
		t.Fatalf("source VIP = %q", got)
	}
}

func TestResolveFailoverV6VIPPrefixesResolvesEachPod(t *testing.T) {
	var calls []string
	gotDst, gotSrc, err := resolveFailoverV6VIPPrefixes("2001:db8::100", func(pod string) (string, error) {
		calls = append(calls, pod)
		if pod == "failover-dst" {
			return "64", nil
		}
		return "56", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotDst != "2001:db8::100/64" || gotSrc != "2001:db8::100/56" {
		t.Fatalf("got destination/source VIPs %q and %q", gotDst, gotSrc)
	}
	if len(calls) != 2 || calls[0] != "failover-dst" || calls[1] != "failover-src" {
		t.Fatalf("prefix resolver calls = %v", calls)
	}
}
