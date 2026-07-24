//go:build linux

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

// Command cni-routed is a CNI plugin that provides fully routed, no-shared-L2
// secondary attachments for KubeVirt VMs (via the built-in bridge binding) and
// routed pods. See pkg/cni for details.

package main

import "github.com/telekom/das-schiff-network-operator/pkg/cni"

func main() {
	cni.PluginMain()
}
