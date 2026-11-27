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
`config/platform-coil` and `config/platform-metallb` on `main`. All images
except `das-schiff-cra-frr` set `USER 65532:65532`, and a `runAsUser: 0` in the
pod spec overrides that. The `cra-frr` image sets no `USER` and runs as root
even without `runAsUser: 0`.

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
| `agent-cra-frr` | Reaches the CRA API on its link-local address (`169.254.1.0` / `fd00:7:caa5::`) over HTTPS, and runs the netlink metrics and health checks against the host namespace. The configuration itself is sent to the CRA API, not applied locally. |
| `agent-cra-vsr` | Runs the health checks and netlink metrics in the host namespace, and reaches the vSR over NETCONF (SSH over the network). |
| `agent-hbn-l2` | Creates and configures interfaces via netlink and writes `/proc/sys/net/...`. Both act on the caller's network namespace, which must be the host one. |
| `agent-netplan` | Reads `/sys/class/net`, deletes bonds removed from the configuration by writing `/sys/class/net/bonding_masters`, and runs the health checks in the host namespace. Sysfs shows the network devices of the caller's namespace, so this must be the host one. The netplan change itself goes through D-Bus. |
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
| `agent-hbn-l2` | Netlink changes need `CAP_NET_ADMIN`. Container runtimes mount `/proc/sys` read-only for unprivileged containers, so writing `/proc/sys/net` needs a privileged container. |
| `agent-cra-frr`, `agent-cra-vsr` | No privileged operation identified: configuration goes to the CRA (HTTPS API or NETCONF), and the local netlink metrics and health checks only read network state and open TCP connections. Removing `privileged` still needs testing. |
| `agent-netplan` | Calls the host's netplan D-Bus service (`io.netplan.Netplan`), whose system-bus policy is written for root, so the agent runs as UID 0. Before `netplan apply` it deletes removed bonds locally by writing `/sys/class/net/bonding_masters`, which needs `CAP_NET_ADMIN` and a writable `/sys`; container runtimes mount `/sys` read-only for unprivileged containers. |
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
- **`agent-cra-frr`, `agent-cra-vsr`**: they do not change host networking,
  so `privileged` and possibly UID 0 may be removable. Test the metrics, health
  checks and, for `agent-cra-frr`, read access to the client certificate in
  `/etc/cra/certs/`; not yet tested.
- **`agent-netplan`**: it needs root for D-Bus. Without `privileged` it
  additionally needs `NET_ADMIN` and a writable `/sys/class/net` for the local
  bond cleanup; test bond removal before dropping `privileged`. Not yet tested.
- **`agent-hbn-l2`**: `privileged` could be replaced by
  `NET_ADMIN` plus an unmasked, writable `/proc/sys/net` only if the runtime
  supports that; keep `privileged` until tested.
- Make `hostPath` mounts read-only where the workload only reads them
  (`/etc/cra/base-config.yaml`).

Pod Security Admission is enforced per namespace and cannot exempt single
pods. With the default `kube-system` namespace, the `privileged` level then
applies to every system workload there. To narrow that, deploy into a
dedicated namespace labelled `pod-security.kubernetes.io/enforce: privileged`,
or use a policy engine such as Kyverno or Gatekeeper, whose exceptions can
select these workloads by namespace, service account and labels.
