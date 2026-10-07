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

package nl

import (
	"testing"

	"github.com/telekom/das-schiff-network-operator/pkg/workloadcni"
)

// TestWorkloadPortAliasPrefixMatchesCNI pins the datapath's notion of a
// workload port to the alias the CNI actually stamps; the constant is
// duplicated rather than imported to keep the API client out of pkg/nl.
func TestWorkloadPortAliasPrefixMatchesCNI(t *testing.T) {
	if workloadPortAliasPrefix != workloadcni.InfraPortPrefix {
		t.Fatalf("workloadPortAliasPrefix = %q, want workloadcni.InfraPortPrefix %q",
			workloadPortAliasPrefix, workloadcni.InfraPortPrefix)
	}
}
