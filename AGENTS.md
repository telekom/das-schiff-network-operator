# das-schiff-network-operator — Agent Instructions

## OVERVIEW

BGP-EVPN network operator for host-level network configuration. Multi-binary architecture: one central operator derives config revisions, four node agents apply them, plus a FRR metrics exporter and FRR CRA binary.

## STRUCTURE

```
cmd/
├── operator/          # Central controller-manager
├── agent-cra-frr/     # Node agent: FRR Containerized Routing Agent
├── agent-cra-vsr/     # Node agent: 6WIND vSR routing
├── agent-netplan/     # Node agent: netplan host networking
├── agent-hbn-l2/      # Node agent: minimal L2 config (alt to netplan)
├── frr-exporter/      # Prometheus metrics exporter for FRR
└── frr-cra/           # FRR Containerized Routing Agent binary
api/v1alpha1/          # CRD types
controllers/           # Webhook suites and controller entrypoints
pkg/
├── reconciler/        # Reconciliation logic (operator/ and agent-*/)
├── network/           # Network state modeling, netplan client (dbus, direct, dummy)
├── nl/                # Netlink integration (host-level networking)
├── frr/               # FRR config rendering and vty/dbus interaction
├── cra-frr/           # CRA-FRR adapter (generates FRR config from CRDs)
├── cra-vsr/           # CRA-vSR adapter
├── healthcheck/       # Node readiness, taint removal, condition management
├── debounce/          # Event debouncing for reconciler loops
└── helpers/           # Diff, merge, slice, maps, type utilities
config/
├── operator/          # Kustomize for operator Deployment
├── agent-cra-frr/     # Kustomize for FRR agent DaemonSet
├── agent-netplan/     # Kustomize for netplan agent DaemonSet
└── ...                # Per-component Kustomize overlays
```

## WHERE TO LOOK

| Task | Location |
|------|----------|
| Operator reconcilers | `pkg/reconciler/operator/` (config, configrevision, vrf, layer2, bgp) |
| Agent reconcilers | `pkg/reconciler/agent-cra-frr/`, `agent-netplan/`, etc. |
| CRD types | `api/v1alpha1/` |
| Netlink host ops | `pkg/nl/manager.go` |
| FRR config generation | `pkg/frr/` and `pkg/cra-frr/` |
| Netplan DBus integration | `pkg/network/netplan/client/dbus/` |
| Node health/taints | `pkg/healthcheck/` |
| Mock packages | `pkg/nl/mock/`, `pkg/frr/mock/`, `pkg/healthcheck/mock/`, `pkg/monitoring/mock/` |

## CRDs

`NodeNetworkConfig`, `NodeNetplanConfig`, `NetworkConfigRevision`, `VRFRouteConfiguration`, `BGPPeering`, `Layer2NetworkConfiguration`

## CONVENTIONS

- **Multi-binary**: Each component has its own `cmd/` entry, Dockerfile, and Kustomize config
- **6 Dockerfiles** at root — one per deployable component (frr-exporter has no Dockerfile)
- **Gradual rollout**: Operator generates per-node configs, waits for agent success, then proceeds to next node
- **Host integration**: Agents interact with netlink, DBus, FRR — requires privileged containers
- **Mock-heavy testing**: All host interfaces (netlink, dbus, frr) have mock packages for unit tests

## ANTI-PATTERNS

- **Do NOT** test host networking code without mocks — use `pkg/nl/mock/`, `pkg/frr/mock/`
- **Do NOT** mix operator and agent reconciler logic — they run in different binaries

## COMMANDS

```bash
make generate           # Code generation (deepcopy, CRDs)
make manifests          # Regenerate CRD/RBAC manifests
make test               # Unit tests (envtest-based, runs fmt+vet first)
make docker-build       # Build all 6 container images
make kind-load          # Load all images into Kind cluster
```

## NOTES

- Operator manages **gradual rollout** — one node at a time via ConfigRevision status
- Agents run as **DaemonSets** with host networking and NET_ADMIN capabilities
- FRR CRA runs as a **sidecar container** alongside the agent, not managed by this operator
- 6WIND vSR is proprietary — not included in this repo (see https://www.6wind.com)
- `pkg/helpers/` contains generic utility functions (diff, merge, slice, maps) — consider reusing before writing new ones
