---
title: Host privileges
description: >-
  Which host privileges each network-operator workload requests, why, and what
  can be restricted further.
---

# Host privileges

The network-operator configures the node's own network stack, so most of its
workloads run with host access. This page lists what each workload in
`config/` requests and why, so cluster operators can scope admission-policy
exceptions (Pod Security `privileged` level, Kyverno/Gatekeeper rules) to
exactly these pods.

The tables reflect `kubectl kustomize` output of `config/default`,
`config/hbn-standalone`, `config/agent-cra-vsr`, `config/network-sync`,
`config/platform-coil` and `config/platform-metallb` on `main`. All images set
`USER 65532:65532`; a `runAsUser: 0` in the pod spec overrides that.

## Summary

| Workload | Kind | `hostNetwork` | `hostPID` | `privileged` | Run-as UID | `hostPath` / sockets |
|---|---|---|---|---|---|---|
| `operator` | Deployment | yes | yes (see [below](#host-pid-namespace)) | no | 65532 (image default) | none |
| `agent-cra-frr` | DaemonSet | yes | yes | yes | 0 | `/etc/cra/base-config.yaml` (File), `/etc/cra/certs/` (Directory) |
| `agent-cra-vsr` | DaemonSet | yes | yes (see [below](#host-pid-namespace)) | yes | 0 | `/etc/cra/base-config.yaml` (File) |
| `agent-hbn-l2` | DaemonSet | yes | yes (see [below](#host-pid-namespace)) | yes | 0 | none |
| `agent-netplan` | DaemonSet | yes | yes | yes | 0 | `/var/run/dbus/system_bus_socket` (Socket) |
| `cra-frr` (`config/hbn-standalone` only) | DaemonSet | no | no | yes | 0 | `/sys/fs/cgroup` (Directory), `/etc/cra/certs/` (DirectoryOrCreate) |
| `network-sync` | Deployment | no | no | no | non-root | none |
| `platform-coil` | Deployment | no | no | no | 65532 (image default) | none |
| `platform-metallb` | Deployment | no | no | no | 65532 (image default) | none |

No workload sets `hostIPC`, adds individual capabilities, or uses `hostPort`.
`privileged: true` grants all capabilities, so the privileged workloads have no
separate `capabilities` list. Only `network-sync` sets a restricted security
context (`allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`,
`readOnlyRootFilesystem: true`, `runAsNonRoot: true`).

`config/hbn-standalone` replaces the `agent-cra-frr` base-config `hostPath`
with a ConfigMap and removes `agent-netplan`. `cmd/frr-exporter` has no image
or manifest in this repository and is not covered here.

## Reasons per privilege

### Host network namespace

| Workload | Reason |
|---|---|
| `operator` | It configures the node network, so it must not depend on the pod network it manages. Webhook, metrics and health endpoints are served on the host IP. |
| `agent-cra-frr` | Reads and changes links and routes via netlink in the host namespace, runs the netlink-based health checks, and reaches the CRA API on its link-local address (`169.254.1.0` / `fd00:7:caa5::`) over HTTPS. |
| `agent-cra-vsr` | Runs the health checks and netlink metrics in the host namespace, and reaches the vSR over NETCONF (SSH over the network). |
| `agent-hbn-l2` | Creates and configures interfaces via netlink and writes `/proc/sys/net/...`. Both act on the caller's network namespace, which must be the host one. |
| `agent-netplan` | Reads `/sys/class/net` and runs the health checks in the host namespace. The netplan change itself goes through D-Bus. |
| others | Not required. |

### Host PID namespace

No code in this repository reads other processes' `/proc/<pid>` entries,
enters namespaces (`setns`, `nsenter`, `/proc/<pid>/ns`), or signals host
processes. `/proc/sys/net` resolves against the network namespace, not the PID
namespace, and D-Bus needs the socket, not the PID namespace. All `nsenter`
uses in the E2E tests run on the kind node through `docker exec`, not in a pod.

`hostPID: true` is therefore not required by the code. Open PR
[#424](https://github.com/telekom/das-schiff-network-operator/pull/424) sets
`hostPID: false` for `operator`, `agent-hbn-l2` and `agent-cra-vsr`; after it
is merged, the table above changes accordingly. `agent-cra-frr` and
`agent-netplan` keep `hostPID: true` for now and are candidates for the same
change once tested.

### Privileged and root

| Workload | Reason |
|---|---|
| `agent-cra-frr`, `agent-hbn-l2` | Netlink changes need `CAP_NET_ADMIN`. Container runtimes mount `/proc/sys` read-only for unprivileged containers, so writing `/proc/sys/net` needs a privileged container. |
| `agent-cra-vsr` | Netlink metrics and health checks. Configuration goes over NETCONF and does not need host privileges. |
| `agent-netplan` | Calls the host's netplan D-Bus service (`io.netplan.Netplan`), whose system-bus policy is written for root, so the agent runs as UID 0. |
| `cra-frr` | Runs systemd, FRR and the CRA configuration API inside the container, which needs cgroup access and full network administration of the CRA namespace. |
| `operator`, `network-sync`, `platform-*` | Not required. |

### Host paths and sockets

| Mount | Workload | Reason |
|---|---|---|
| `/etc/cra/base-config.yaml` | `agent-cra-frr`, `agent-cra-vsr` | Node-specific base configuration written by node provisioning. |
| `/etc/cra/certs/` | `agent-cra-frr`, `cra-frr` | Client and server certificates for the mTLS connection between agent and CRA API. |
| `/var/run/dbus/system_bus_socket` | `agent-netplan` | Applies netplan configuration through the host's D-Bus. |
| `/sys/fs/cgroup` | `cra-frr` | systemd inside the CRA container. |

## What can be restricted further

- **`hostPID`**: drop it for `operator`, `agent-hbn-l2` and `agent-cra-vsr`
  (PR #424), then test the same for `agent-cra-frr` and `agent-netplan`.
- **`operator`**: it needs no Linux privileges. Add `runAsNonRoot: true`,
  `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true` and
  `capabilities.drop: [ALL]`, as `network-sync` already does. The same applies
  to `platform-coil` and `platform-metallb`.
- **`agent-cra-vsr`**: it does not change host networking, so `privileged` may
  be replaceable by a small capability set (for example `NET_ADMIN`); not yet
  tested.
- **`agent-netplan`**: it needs root for D-Bus, but probably not `privileged`;
  not yet tested.
- **`agent-cra-frr`, `agent-hbn-l2`**: `privileged` could be replaced by
  `NET_ADMIN` plus an unmasked, writable `/proc/sys/net` only if the runtime
  supports that; keep `privileged` until tested.
- Make `hostPath` mounts read-only where the workload only reads them
  (`/etc/cra/base-config.yaml`).

Keep these pods in a dedicated namespace (by default `kube-system`) and scope
any admission-policy exception to their service account and labels.
