---
title: Node Readiness
description: >-
  The NetworkOperatorReady node condition, taint removal, and health-check
  configuration.
---

# Node Readiness

During initialization, nodes may be tainted to prevent premature workload
scheduling while the network stack is being prepared. Once the operator's health
checks pass, it removes the configured taints and sets a standard node condition
that higher-level automation can rely on.

## The `NetworkOperatorReady` condition

When all health checks (interface state, reachability targets, API-server access)
pass, the configured taints are removed and a custom node condition is created or
updated:

```text
Type:    NetworkOperatorReady
Status:  True | False
Reason:  <see below>
Message: Human readable description of the last evaluation.
```

Inspect it with:

```bash
kubectl get node <node> -o jsonpath='{.status.conditions[?(@.type=="NetworkOperatorReady")]}' | jq
```

!!! note "Taints are not re-applied on failure"
    When a health check later fails, the condition is set to `False` with the
    corresponding reason, but the init taints are **not** re-applied — this
    avoids disruptive rescheduling. The condition provides ongoing status
    instead.

### Reasons

Common reasons:

| Reason | Meaning |
|---|---|
| `HealthChecksPassed` | All checks succeeded |
| `InterfaceCheckFailed` | One or more configured interfaces are not `UP` (supports glob patterns like `eth*`, `bond?`) |
| `ReachabilityCheckFailed` | A configured reachability target is unreachable |
| `APIServerCheckFailed` | Cannot reach the Kubernetes API server |

Agent-specific reasons:

| Reason | Agent | Meaning |
|---|---|---|
| `NetplanInitializationFailed` / `NetplanApplyFailed` | `agent-netplan` | netplan errors |
| `VLANReconcileFailed` / `LoopbackReconcileFailed` | `agent-hbn-l2` | hbn-l2 errors |
| `ConfigFetchFailed` | any | Failed to fetch node configuration |

## Configuring taints

The taints removed once the network stack is ready are configured in the
healthcheck configuration file (`/opt/network-operator/net-healthcheck-config.yaml`)
under the `taints` field:

```yaml
taints:
  - node.cloudprovider.kubernetes.io/uninitialized
  - node.t-caas.telekom.com/uninitialized
```

## Splitting configuration across files

The configuration can be split across multiple files using external sources
(`interfacesFile`, `reachabilityFile`, `taintsFile`). This is useful for reading
parts of the configuration from hostPath mounts or ConfigMaps. Missing files are
silently ignored, enabling graceful fallback with `FileOrCreate` volume
strategies.

A sample healthcheck configuration file is provided in the repository at
[`config/samples/net-healthcheck-config.yaml`](https://github.com/telekom/das-schiff-network-operator/blob/main/config/samples/net-healthcheck-config.yaml).

## Related

- [Metrics](metrics.md) — the `nwop_healthcheck_*` metrics expose the same state.
- [Debugging](../advanced/debugging.md) — inspecting node-level configuration.
