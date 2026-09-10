//go:build linux

/*
Copyright 2025.

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

package cni

import (
	"net"
	"reflect"
	"testing"

	current "github.com/containernetworking/cni/pkg/types/100"
)

func TestPodIdentity(t *testing.T) {
	ns, name := podIdentity("IgnoreUnknown=1;K8S_POD_NAMESPACE=demo;K8S_POD_NAME=vm-launcher;K8S_POD_INFRA_CONTAINER_ID=abc")
	if ns != "demo" || name != "vm-launcher" {
		t.Fatalf("unexpected identity ns=%q name=%q", ns, name)
	}

	ns, name = podIdentity("")
	if ns != "" || name != "" {
		t.Fatalf("expected empty identity, got ns=%q name=%q", ns, name)
	}
}

func TestHostRoutes(t *testing.T) {
	result := &current.Result{
		IPs: []*current.IPConfig{
			{Address: net.IPNet{IP: net.ParseIP("10.201.0.10"), Mask: net.CIDRMask(32, 32)}},
			{Address: net.IPNet{IP: net.ParseIP("fd00:201::10"), Mask: net.CIDRMask(128, 128)}},
		},
	}
	got := hostRoutes(result)
	want := []string{"10.201.0.10/32", "fd00:201::10/128"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hostRoutes = %v, want %v", got, want)
	}
}
