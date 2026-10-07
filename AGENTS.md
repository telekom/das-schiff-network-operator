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

The library's [`docs/upstream-libraries.md`](https://github.com/telekom/t-caas-go-library/blob/main/docs/upstream-libraries.md)
has detailed decisions and compatibility caveats; it is currently private and
is planned to become public. This condensed table is included here so this
guidance remains useful until then. Relevant merged packages include `pkg/netutil`,
`pkg/remoteclient`, `pkg/discovery/tracker`, and `pkg/patch`; check the guide and
package docs for availability and exact semantics before adopting others.

Convenience wrappers are justified only when the same glue demonstrably
repeats across multiple repositories. Contribute that shared wrapper to
`telekom/t-caas-go-library` instead of duplicating it here. Keep this operator's
domain policy and configuration local.

### Existing migration candidates

These are candidates for separate migration work, not changes in this
documentation update. Preserve the behavior noted when evaluating replacements:

- `pkg/reconciler/intent/ipmath/ipmath.go` implements address/prefix behavior
  that may use `pkg/netutil`; retain its `/32` rejection and verify IPv4/IPv6
  edge cases.
- `controllers/sync/remote_client.go` maintains remote clients; compare with
  `pkg/remoteclient`, while keeping namespace enumeration local.
- `controllers/shared/predicates.go` builds name predicates; consider
  `predicate.NewPredicateFuncs` and retain the intended event behavior.
- `pkg/debounce/debounce.go` implements delayed reconciliation; evaluate the
  typed client-go workqueue rather than creating another timer engine.
- `e2etests/framework/cluster.go` decodes/applies manifests and polls
  resources; consider the e2e-framework decoder and waits while preserving
  namespace overrides, cleanup, and resource-specific readiness checks.
- `pkg/monitoring/collector.go` has custom collector registration and
  collection; use the Prometheus registry/collectors directly where applicable,
  retaining operator-specific metrics and scrape behavior.
