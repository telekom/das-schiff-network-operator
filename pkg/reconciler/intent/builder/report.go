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

import "context"

// BuildIssue records an intent resource that a builder skipped because of a
// data error (e.g. an ambiguous AnnouncementPolicy or a route-target
// collision). The reconciler surfaces these as a Ready=False condition on the
// offending resource so the failure is visible to operators rather than only
// appearing in the controller log.
type BuildIssue struct {
	// Kind is the intent CRD kind (e.g. "Layer2Attachment").
	Kind string
	// Name is the offending resource's name.
	Name string
	// Reason is a short PascalCase condition reason (e.g. "AmbiguousAnnouncementPolicy").
	Reason string
	// Message is a human-readable explanation.
	Message string
}

// BuildReport accumulates the resources skipped during a single reconcile pass.
// Builders run sequentially within the reconciler, so it is not concurrency-safe.
type BuildReport struct {
	issues []BuildIssue
}

// NewBuildReport creates an empty BuildReport.
func NewBuildReport() *BuildReport {
	return &BuildReport{}
}

// Issues returns the accumulated skip issues.
func (r *BuildReport) Issues() []BuildIssue {
	if r == nil {
		return nil
	}
	return r.issues
}

func (r *BuildReport) add(issue BuildIssue) {
	r.issues = append(r.issues, issue)
}

type reportContextKey struct{}

// WithReport returns a context carrying the given BuildReport so builders can
// record skipped resources via reportSkip.
func WithReport(ctx context.Context, report *BuildReport) context.Context {
	return context.WithValue(ctx, reportContextKey{}, report)
}

// reportSkip records a skipped resource on the BuildReport carried by ctx, if
// any. It is a no-op when no report is present (e.g. in unit tests that call a
// builder directly), so builders can call it unconditionally.
func reportSkip(ctx context.Context, kind, name, reason, message string) {
	report, ok := ctx.Value(reportContextKey{}).(*BuildReport)
	if !ok || report == nil {
		return
	}
	report.add(BuildIssue{Kind: kind, Name: name, Reason: reason, Message: message})
}
