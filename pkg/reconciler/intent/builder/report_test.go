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

package builder

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

func TestBuildReport_CapturesSkippedResource(t *testing.T) {
	data := baseInboundData()
	data.Inbounds = []nc.Inbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-inbound"},
			Spec: nc.InboundSpec{
				NetworkRef:   "nonexistent",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}},
			},
		},
	}

	report := NewBuildReport()
	ctx := WithReport(context.Background(), report)

	b := NewInboundBuilder()
	if _, err := b.Build(ctx, data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	issues := report.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected 1 build issue, got %d: %+v", len(issues), issues)
	}
	got := issues[0]
	if got.Kind != "Inbound" || got.Name != "bad-inbound" || got.Reason != "NetworkNotFound" {
		t.Errorf("unexpected issue: %+v", got)
	}
	if got.Message == "" {
		t.Error("expected a non-empty issue message")
	}
}

func TestBuildReport_NoIssuesWhenHealthy(t *testing.T) {
	data := baseInboundData()
	data.Inbounds = []nc.Inbound{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "good-inbound"},
			Spec: nc.InboundSpec{
				NetworkRef:   "net-1",
				Destinations: &metav1.LabelSelector{MatchLabels: map[string]string{"type": "gw"}},
				Addresses:    &nc.AddressAllocation{IPv4: []string{"10.250.1.0/24"}},
			},
		},
	}

	report := NewBuildReport()
	ctx := WithReport(context.Background(), report)

	b := NewInboundBuilder()
	if _, err := b.Build(ctx, data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Issues()) != 0 {
		t.Errorf("expected no build issues, got %+v", report.Issues())
	}
}

func TestReportSkip_NoopWithoutReport(_ *testing.T) {
	// Must not panic when ctx carries no report (e.g. unit tests calling Build directly).
	reportSkip(context.Background(), "Inbound", "x", "Reason", "message")
}
