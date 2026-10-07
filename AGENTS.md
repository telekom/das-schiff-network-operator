# Agent guidance

## Reuse upstream libraries before writing helpers

This rule applies to humans and AI agents, whether or not a dependency-adoption
PR has merged. Before adding a helper, check in this order:

1. Go standard library.
2. Kubernetes APIs and libraries: `k8s.io/apimachinery`,
   `k8s.io/client-go`, and `sigs.k8s.io/controller-runtime`.
3. Flux packages under `github.com/fluxcd/pkg`.
4. Other well-known, maintained upstream libraries.
5. Relevant packages already merged in
   [`telekom/t-caas-go-library`](https://github.com/telekom/t-caas-go-library).
6. Custom code only when none of these fits.

The paths below are package imports to evaluate, not dependencies added by
this guidance. Check compatibility with this repository's Go and Kubernetes
versions and preserve domain-specific behavior.

| Concern | Upstream package |
| --- | --- |
| IP addresses, prefixes, and overlap | `net/netip` |
| Prefix ranges and IP sets | `go4.org/netipx` |
| Linux links, addresses, and routes | `github.com/vishvananda/netlink` |
| Kubernetes IP compatibility helpers | `k8s.io/utils/net` |
| Kubernetes conflict retry and polling | `k8s.io/client-go/util/retry`; `k8s.io/apimachinery/pkg/util/wait` |
| Controller predicates, event mapping, and indexes | `sigs.k8s.io/controller-runtime/pkg/predicate`; `sigs.k8s.io/controller-runtime/pkg/handler`; `sigs.k8s.io/controller-runtime/pkg/client` |
| Owner references and finalizers | `sigs.k8s.io/controller-runtime/pkg/controller/controllerutil` |
| Kubernetes object patch and apply | `sigs.k8s.io/controller-runtime/pkg/client`; `k8s.io/client-go/applyconfigurations` |
| Conditions and condition slices | `github.com/fluxcd/pkg/runtime/conditions`; `k8s.io/apimachinery/pkg/api/meta` |
| Reconcile queues, rate limiting, and delayed work | `k8s.io/client-go/util/workqueue` |
| E2E resource waits and manifest decoding | `sigs.k8s.io/e2e-framework/klient/wait`; `sigs.k8s.io/e2e-framework/klient/decoder` |
| Metrics and recording | `github.com/prometheus/client_golang/prometheus`; `github.com/fluxcd/pkg/runtime/metrics` |
| Context-aware logging | `log/slog`; `sigs.k8s.io/controller-runtime/pkg/log` |
| YAML decoding | `sigs.k8s.io/yaml`; `gopkg.in/yaml.v2` |
| Webhook certificate rotation | `github.com/open-policy-agent/cert-controller/pkg/rotator` |
| IP arithmetic and repeated address conventions | `github.com/telekom/t-caas-go-library/pkg/netutil` |
| Remote Kubernetes client registry | `github.com/telekom/t-caas-go-library/pkg/remoteclient` |
| Kubernetes discovery tracking | `github.com/telekom/t-caas-go-library/pkg/discovery/tracker` |
| Repeated Kubernetes patch retry composition | `github.com/telekom/t-caas-go-library/pkg/patch` |

The public library's [`docs/upstream-libraries.md`](https://github.com/telekom/t-caas-go-library/blob/main/docs/upstream-libraries.md)
has detailed decisions and compatibility caveats. Relevant merged packages include `pkg/netutil`,
`pkg/remoteclient`, `pkg/discovery/tracker`, and `pkg/patch`; check the guide and
package docs for availability and exact semantics before adopting others.

Convenience wrappers are justified only when the same glue demonstrably
repeats across multiple repositories. Contribute that shared wrapper to
`telekom/t-caas-go-library` instead of duplicating it here. Keep this operator's
domain policy and configuration local.

### Adoption status and remaining candidates

The following migrations are in open, unmerged PRs. Until they merge, the default
branch still uses the local implementations; do not duplicate that work:

- `pkg/reconciler/intent/ipmath/ipmath.go`: gateway derivation delegates to
  `github.com/telekom/t-caas-go-library/pkg/netutil.FirstUsable` in the adoption
  PR. Keep the local formatting and error context, `/32` and `/128` rejection,
  and `/31` and `/127` point-to-point behavior.
- `pkg/debounce/debounce.go`: the workqueue migration uses the typed client-go
  delaying queue and joins workers on shutdown.
- `e2etests/framework/wait.go` and `e2e/setup/exec.go`: the polling migration
  uses apimachinery wait while keeping their different error semantics.
- Collection helpers: the stdlib migration uses `slices` and `maps` at callers
  and removes unused slice helpers; do not add new helpers for those operations.

Other candidates need separate semantic evaluation:

- `controllers/sync/remote_client.go` accepts plugin/filesystem kubeconfigs
  and enumerates clients by namespace. These behaviors are not provided by
  `pkg/remoteclient`; it is not a drop-in replacement.
- `controllers/shared/predicates.go` accepts only matching create/update
  events and rejects delete/generic events. `predicate.NewPredicateFuncs`
  alone does not preserve that event behavior.
- `e2etests/framework/cluster.go` decodes/applies manifests and polls
  resources; consider the e2e-framework decoder and waits while preserving
  namespace overrides, cleanup, and resource-specific readiness checks.
- `pkg/monitoring/collector.go` already implements native Prometheus collectors.
  Keep its domain-specific scrape success/duration metrics and collector fan-out.
