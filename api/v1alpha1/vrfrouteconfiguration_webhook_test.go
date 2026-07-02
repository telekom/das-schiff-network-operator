/*
Copyright 2022.

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

package v1alpha1

import (
	"testing"
)

func intPtr(i int) *int {
	return &i
}

func TestFindDuplicates(t *testing.T) {
	tests := []struct {
		name     string
		items    []VrfRouteConfigurationPrefixItem
		wantDups int
	}{
		{
			name:     "no items",
			items:    []VrfRouteConfigurationPrefixItem{},
			wantDups: 0,
		},
		{
			name: "no duplicates - different CIDRs",
			items: []VrfRouteConfigurationPrefixItem{
				{CIDR: "10.0.0.0/8", Action: "permit"},
				{CIDR: "172.16.0.0/12", Action: "permit"},
			},
			wantDups: 0,
		},
		{
			name: "exact duplicates",
			items: []VrfRouteConfigurationPrefixItem{
				{CIDR: "10.0.0.0/8", Action: "permit"},
				{CIDR: "10.0.0.0/8", Action: "permit"},
			},
			wantDups: 1,
		},
		{
			name: "same CIDR different action - not duplicates",
			items: []VrfRouteConfigurationPrefixItem{
				{CIDR: "::/0", Action: "deny"},
				{CIDR: "::/0", Action: "permit", GE: intPtr(44), LE: intPtr(128)},
			},
			wantDups: 0,
		},
		{
			name: "same CIDR and action but different ge/le - not duplicates",
			items: []VrfRouteConfigurationPrefixItem{
				{CIDR: "10.0.0.0/8", Action: "permit", LE: intPtr(32)},
				{CIDR: "10.0.0.0/8", Action: "permit", GE: intPtr(16), LE: intPtr(24)},
			},
			wantDups: 0,
		},
		{
			name: "issue 154 - deny default route + permit with ge/le",
			items: []VrfRouteConfigurationPrefixItem{
				{CIDR: "::/0", Action: "deny"},
				{CIDR: "10.0.0.0/16", Action: "permit", LE: intPtr(32)},
				{CIDR: "::/0", Action: "permit", GE: intPtr(44), LE: intPtr(128)},
			},
			wantDups: 0,
		},
		{
			name: "true duplicates with ge/le",
			items: []VrfRouteConfigurationPrefixItem{
				{CIDR: "::/0", Action: "permit", GE: intPtr(44), LE: intPtr(128)},
				{CIDR: "::/0", Action: "permit", GE: intPtr(44), LE: intPtr(128)},
			},
			wantDups: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dups := findDuplicates(tt.items)
			if len(dups) != tt.wantDups {
				t.Errorf("findDuplicates() returned %d duplicates %v, want %d", len(dups), dups, tt.wantDups)
			}
		})
	}
}

func TestValidateItemList(t *testing.T) {
	tests := []struct {
		name    string
		items   []VrfRouteConfigurationPrefixItem
		wantErr bool
	}{
		{
			name:    "empty list is valid",
			items:   []VrfRouteConfigurationPrefixItem{},
			wantErr: false,
		},
		{
			name: "issue 154 scenario - same CIDR with different action/ge/le is valid",
			items: []VrfRouteConfigurationPrefixItem{
				{CIDR: "::/0", Action: "deny", Seq: 1},
				{CIDR: "10.0.0.0/16", Action: "permit", LE: intPtr(32), Seq: 2},
				{CIDR: "::/0", Action: "permit", GE: intPtr(44), LE: intPtr(128), Seq: 3},
			},
			wantErr: false,
		},
		{
			name: "exact duplicate entries are rejected",
			items: []VrfRouteConfigurationPrefixItem{
				{CIDR: "::/0", Action: "deny", Seq: 1},
				{CIDR: "::/0", Action: "deny", Seq: 2},
			},
			wantErr: true,
		},
		{
			name: "duplicate seq is rejected",
			items: []VrfRouteConfigurationPrefixItem{
				{CIDR: "10.0.0.0/8", Action: "permit", Seq: 1},
				{CIDR: "172.16.0.0/12", Action: "permit", Seq: 1},
			},
			wantErr: true,
		},
		{
			name: "invalid CIDR is rejected",
			items: []VrfRouteConfigurationPrefixItem{
				{CIDR: "not-a-cidr", Action: "permit", Seq: 1},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateItemList(tt.items)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateItemList() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
