package framework

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type execReply struct {
	stdout string
	stderr string
	err    error
}

type fakePodExecutor struct {
	calls   [][]string
	replies []execReply
}

func (f *fakePodExecutor) ExecInPod(_ context.Context, _, _, _ string, command []string) (stdout, stderr string, err error) {
	f.calls = append(f.calls, append([]string(nil), command...))
	if len(f.calls) > len(f.replies) {
		return "", "", errors.New("unexpected command")
	}
	reply := f.replies[len(f.calls)-1]
	return reply.stdout, reply.stderr, reply.err
}

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

func TestEnsureIPv6NoDadAcceptsPreconfiguredDADAndPreservesPrefix(t *testing.T) {
	exec := &fakePodExecutor{replies: []execReply{
		{stdout: "3: net1 inet6 2001:db8::42/56 scope global\n"},
		{stderr: "read-only", err: errors.New("sysctl write failed")},
		{stdout: "0\n"},
		{},
		{},
	}}

	if err := ensureIPv6NoDad(context.Background(), exec, "ns", "pod", "2001:db8::42", "net1"); err != nil {
		t.Fatalf("ensureIPv6NoDad() error = %v", err)
	}

	want := [][]string{
		{"ip", "-6", "-o", "addr", "show", "dev", "net1"},
		{"sysctl", "-w", "net.ipv6.conf.net1.accept_dad=0"},
		{"sysctl", "-n", "net.ipv6.conf.net1.accept_dad"},
		{"ip", "addr", "del", "2001:db8::42/56", "dev", "net1"},
		{"ip", "addr", "add", "2001:db8::42/56", "dev", "net1"},
	}
	if !reflect.DeepEqual(exec.calls, want) {
		t.Fatalf("commands = %#v, want %#v", exec.calls, want)
	}
}

func TestEnsureIPv6NoDadPreservesWriteErrorWhenFallbackIsUnsafe(t *testing.T) {
	writeErr := errors.New("sysctl write failed")
	for name, readReply := range map[string]execReply{
		"nonzero":    {stdout: "1\n"},
		"read error": {stderr: "read failed", err: errors.New("sysctl read failed")},
	} {
		t.Run(name, func(t *testing.T) {
			exec := &fakePodExecutor{replies: []execReply{
				{stdout: "3: net1 inet6 2001:db8::42/56 scope global\n"},
				{stderr: "read-only", err: writeErr},
				readReply,
			}}

			err := ensureIPv6NoDad(context.Background(), exec, "ns", "pod", "2001:db8::42", "net1")
			if !errors.Is(err, writeErr) {
				t.Fatalf("error = %v, want original write error", err)
			}
			if len(exec.calls) != 3 {
				t.Fatalf("commands = %#v, want no address mutation after unsafe fallback", exec.calls)
			}
		})
	}
}
