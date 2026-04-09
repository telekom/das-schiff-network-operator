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

// Package platform contains controllers for platform integrations
// (MetalLB, Coil, InterfaceConfig) that manage their own K8s resources
// independently from the NNC pipeline. These run as separate controllers
// with eventual consistency — it's fine if MetalLB pools exist before
// the VRF is plumbed on nodes.
package platform
