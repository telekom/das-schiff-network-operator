/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ipam

import (
	"reflect"
	"sort"
	"testing"
)

func TestAllocateSubnet_FreshAllocation(t *testing.T) {
	res, err := AllocateSubnet("10.0.0.0/29", []string{"node-c", "node-a", "node-b"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"node-a": "10.0.0.1",
		"node-b": "10.0.0.2",
		"node-c": "10.0.0.3",
	}
	if !reflect.DeepEqual(res.Updated, want) {
		t.Errorf("Updated = %v, want %v", res.Updated, want)
	}
	if len(res.Removed) != 0 || len(res.Unallocated) != 0 {
		t.Errorf("expected no removed/unallocated, got %+v", res)
	}
}

func TestAllocateSubnet_PreservesExisting(t *testing.T) {
	existing := map[string]string{
		"node-b": "10.0.0.5",
		"node-a": "10.0.0.3",
	}
	res, err := AllocateSubnet("10.0.0.0/29", []string{"node-a", "node-b", "node-c"}, existing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Updated["node-a"] != "10.0.0.3" {
		t.Errorf("expected node-a preserved at 10.0.0.3, got %s", res.Updated["node-a"])
	}
	if res.Updated["node-b"] != "10.0.0.5" {
		t.Errorf("expected node-b preserved at 10.0.0.5, got %s", res.Updated["node-b"])
	}
	// node-c gets the lowest unused address (10.0.0.1).
	if res.Updated["node-c"] != "10.0.0.1" {
		t.Errorf("expected node-c at 10.0.0.1, got %s", res.Updated["node-c"])
	}
}

func TestAllocateSubnet_NodeRemoval(t *testing.T) {
	existing := map[string]string{
		"node-a": "10.0.0.1",
		"node-b": "10.0.0.2",
	}
	res, err := AllocateSubnet("10.0.0.0/29", []string{"node-a"}, existing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := res.Updated["node-b"]; ok {
		t.Errorf("expected node-b removed, still in Updated: %v", res.Updated)
	}
	if got := res.Removed; len(got) != 1 || got[0] != "node-b" {
		t.Errorf("expected Removed=[node-b], got %v", got)
	}
}

func TestAllocateSubnet_Idempotent(t *testing.T) {
	nodes := []string{"node-a", "node-b", "node-c"}
	res1, err := AllocateSubnet("10.0.0.0/29", nodes, nil)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	res2, err := AllocateSubnet("10.0.0.0/29", nodes, res1.Updated)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !reflect.DeepEqual(res1.Updated, res2.Updated) {
		t.Errorf("second call drifted: %v vs %v", res1.Updated, res2.Updated)
	}
	if len(res2.Removed) != 0 {
		t.Errorf("idempotent call should not remove: %v", res2.Removed)
	}
}

func TestAllocateSubnet_Exhaustion(t *testing.T) {
	// /30 yields 2 usable hosts (.1 and .2; .0 net, .3 broadcast).
	res, err := AllocateSubnet("10.0.0.0/30", []string{"a", "b", "c"}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(res.Updated) != 2 {
		t.Errorf("expected 2 allocations, got %v", res.Updated)
	}
	if got := res.Unallocated; len(got) != 1 || got[0] != "c" {
		t.Errorf("expected Unallocated=[c], got %v", got)
	}
}

func TestAllocateSubnet_IPv6(t *testing.T) {
	res, err := AllocateSubnet("fd00::/126", []string{"a", "b"}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	keys := make([]string, 0, len(res.Updated))
	for k := range res.Updated {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != 2 {
		t.Errorf("expected 2 v6 allocations, got %v", res.Updated)
	}
	// First host of fd00::/126 is fd00::1 (skip network fd00::).
	if res.Updated["a"] != "fd00::1" {
		t.Errorf("expected a=fd00::1, got %s", res.Updated["a"])
	}
}

func TestAllocateSubnet_InvalidCIDR(t *testing.T) {
	if _, err := AllocateSubnet("not-a-cidr", []string{"a"}, nil); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}
