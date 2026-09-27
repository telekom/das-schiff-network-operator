package framework

import (
	"reflect"
	"testing"
)

func TestResetIPv6AddressDeletesAndReadds(t *testing.T) {
	var commands [][]string
	if err := resetIPv6Address(func(command []string) (string, string, error) {
		commands = append(commands, command)
		return "", "", nil
	}, "fd94::10", "eth0"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"ip", "addr", "del", "fd94::10/64", "dev", "eth0"},
		{"ip", "addr", "add", "fd94::10/64", "dev", "eth0"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}
