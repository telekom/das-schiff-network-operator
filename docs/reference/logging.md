---
title: Logging
description: Log level and format flags of every network-operator binary, and what each level logs.
---

# Logging

Every binary of the network-operator logs through one
[controller-runtime](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/log/zap)
zap logger. Its level and format are set with the same `--zap-*` flags in
every binary. The lines of the libraries the binaries use (logrus in the
netplan client, klog in client-go) are routed into the same logger, so the
flags control all log output.

The [message catalogue](#message-catalogue) at the end of this page is
generated from the log calls in the Go code by `go run ./hack/logref`. The
unit test in `hack/logref` (part of `make test`) fails when the catalogue does
not match the code, so a pull request that adds, removes or changes a log
message, level or field key must regenerate it. The same test also fails
when:

- a log call has no literal message (use a constant message and key/value
  fields for the variable parts),
- code imports the standard library `log` package, whose output would ignore
  the flags,
- a binary in `cmd/` does not call `logging.RedirectLibraryLoggers`.

## Flags

| Flag | Values | Default | Effect |
|------|--------|---------|--------|
| `--zap-log-level` | `debug`, `info`, `error`, `panic`, or an integer `N > 0` | `debug` | Lowest level written. `debug` is the same as `1` (logr `V(1)`); `N` writes `V(0)` to `V(N)`. `warn` is not accepted. |
| `--zap-encoder` | `console`, `json` | `console` | `console`: tab-separated text for people. `json`: one JSON object per line for log collectors. |
| `--zap-devel` | `true`, `false` | `true` | Development profile: `console`, `debug`, stack traces from `warn`. `false` selects the production profile: `json`, `info`, stack traces from `error`, sampling. Explicit `--zap-encoder`, `--zap-log-level` and `--zap-stacktrace-level` win over the profile. |
| `--zap-stacktrace-level` | `info`, `error`, `panic` | `warn` (development); `panic` in `frr-cra` | Level from which a stack trace is attached to the line. `frr-cra` defaults to `panic` because it retries failed operations in a loop and would otherwise print a stack trace with every error line. |
| `--zap-time-encoding` | `epoch`, `millis`, `nano`, `iso8601`, `rfc3339`, `rfc3339nano` | `rfc3339` | Format of the `ts` field. |

All binaries start in the development profile, so by default they write every
`info`, `error` and `debug` (`V(1)`) line as console text to stderr. For
production, `--zap-log-level=info --zap-encoder=json` (or `--zap-devel=false`)
drops the debug lines and makes the output machine-readable.

In the `json` format each line has the keys `level`, `ts`, `logger` (the
logger name, for example `setup`, `frr-cra`, `neighborsync`, `logrus` or
`klog`), `caller` (development profile only), `msg`, `error` (error lines) and
the structured fields listed in the catalogue. The `console` format writes the
same content as tab-separated text; its layout is not stable. The production
profile samples: per message and level, the first 100 lines per second are
written, then every 100th.

## Binaries

| Binary | Runs as | How to set the flags |
|--------|---------|----------------------|
| `operator` | Deployment `operator` (`config/operator`) | container `args` |
| `agent-cra-frr` | DaemonSet `agent-cra-frr` (`config/agent-cra-frr`) | container `args` |
| `agent-cra-vsr` | DaemonSet `agent-cra-vsr` (`config/agent-cra-vsr`) | container `args` |
| `agent-hbn-l2` | DaemonSet `agent-hbn-l2` (`config/agent-hbn-l2`) | container `args` |
| `agent-netplan` | DaemonSet `agent-netplan` (`config/agent-netplan`) | container `args` |
| `network-sync` | Deployment `network-sync` (`config/network-sync`) | container `args` |
| `platform-coil` | Deployment `platform-coil` (`config/platform-coil`) | container `args` |
| `platform-metallb` | Deployment `platform-metallb` (`config/platform-metallb`) | container `args` |
| `frr-cra` | systemd unit `frr-cra.service` in the CRA-FRR image; logs go to the journal (`journalctl -u frr-cra`) | `ARGS` in `/etc/default/frr-cra`, for example `ARGS="--zap-log-level=info --zap-encoder=json"` |
| `frr-exporter` | standalone FRR metrics exporter | command line |
| `kubectl-nnc` | kubectl plugin (CLI) | command line. Without a `--zap-*` flag it sets no logger and prints only its output and errors; any `--zap-*` flag turns on library logging on stderr with the defaults above. |

The manifests in `config/` set no `--zap-*` flag, so the defaults apply. To
change the level of a deployed binary, add the flags with a kustomize patch,
for example for the operator:

```yaml
# kustomization.yaml
patches:
  - target:
      kind: Deployment
      name: operator
    patch: |-
      - op: add
        path: /spec/template/spec/containers/0/args/-
        value: --zap-log-level=info
      - op: add
        path: /spec/template/spec/containers/0/args/-
        value: --zap-encoder=json
```

There is no per-component level and no runtime switch: the level applies to
the whole process and changing it restarts the pods.

## Levels

| Level | What is logged | When to use |
|-------|----------------|-------------|
| `error` | A reconcile, a request to the CRA, a netlink, FRR, netplan or D-Bus operation, or a Kubernetes API call failed; the binary retries or reports the failure upwards. Startup failures are logged at `error` before the binary exits with status 1. | always on |
| `info` | Lifecycle and state changes: startup and configuration, controller and manager start, leader election, applied node configurations, created and deleted VRFs, Layer2s and policy rules, FRR reloads, health check transitions, webhook validations, conditions handled without failing (for example an unsupported address type, or a timeout waiting for L3VNIs). logrus `warn` lines are logged at `info`. | production |
| `debug` (`V(1)`) | Per-event detail with high volume: every kernel neighbour update and BPF neighbour event (`frr-cra`), BPF program attachment, intent finalizer changes, sync progress, and controller-runtime's own `V(1)` lines. | troubleshooting |
| `V(2)`, `V(3)` | logrus `debug` lines (`V(2)`: netplan D-Bus apply steps, the netplan paths being set, network link lookups) and logrus `trace` lines (`V(3)`), plus some controller-runtime and client-go lines. Enable with `--zap-log-level=2` or `3`. They stay hidden at the default level, as they were with logrus' default `info` level. The netplan settings values are not logged because they can contain secrets such as WireGuard private keys. | deep debugging |

Per binary, the [message counts](#messages-per-binary) show how many distinct
messages each level can produce, and the
[package tables](#messages-per-package) list every message.

### Lines from libraries

These lines come from libraries, so the catalogue does not list them:

| Source | Binaries | Level | Logger name |
|--------|----------|-------|-------------|
| controller-runtime (manager, controllers, webhooks, cache, metrics server) | all except `frr-cra`, `frr-exporter` and `kubectl-nnc` | `info`, `error`; `V(1)` at `debug` | for example `controller-runtime.metrics`, plus the `controller`, `controllerGroup`, `controllerKind`, `name`, `namespace`, `reconcileID` fields on reconciler lines |
| client-go (klog), for example leader election and reflector errors | all that talk to the API server | `info`, `error`; klog `V(n)` lines are not written | `klog` |
| logrus (netplan client in `pkg/network`) | see the `logrus` column of the [message counts](#messages-per-binary) | `info`/`warn` at `info`, `error`/`fatal`/`panic` at `error`, `debug` at `V(2)`, `trace` at `V(3)` | `logrus` |

### Other output

- At startup `operator`, the four agents and `frr-exporter` print one
  version line (`<binary> info - version: …, commit: …`) to stdout,
  independent of the log flags.
- The netplan D-Bus client (`pkg/network/netplan/client/dbus`) can write
  every D-Bus call as a `busctl` command line to a separate, rotated file
  (`dbus.Opts.ExecutionLogPath`). No binary enables this trace today, and the
  log flags do not affect it.
- `kubectl-nnc` writes its tree and table output to stdout and errors to
  stderr.

## Message catalogue

The tables below are generated; do not edit them by hand. Run
`go run ./hack/logref` after changing log calls.

<!-- BEGIN GENERATED: go run ./hack/logref -->

### Messages per binary

Number of distinct messages each binary can log, per level. `logrus` marks binaries that link code logging through logrus.

| Binary | error | info | debug | V(2) and higher | logrus |
|---|---|---|---|---|---|
| `agent-cra-frr` | 40 | 41 | 1 | 1 | yes |
| `agent-cra-vsr` | 40 | 41 | 1 | 1 | yes |
| `agent-hbn-l2` | 7 | 11 | 1 | 1 | yes |
| `agent-netplan` | 12 | 28 | 1 | 5 | yes |
| `frr-cra` | 64 | 39 | 7 | 1 | yes |
| `frr-exporter` | 27 | 26 | 0 | 1 | yes |
| `kubectl-nnc` | 0 | 6 | 0 | 1 | yes |
| `network-sync` | 12 | 16 | 2 | 0 | no |
| `operator` | 44 | 75 | 9 | 1 | yes |
| `platform-coil` | 9 | 30 | 0 | 1 | yes |
| `platform-metallb` | 9 | 30 | 0 | 1 | yes |

### Messages per package

Messages are the literal message (logr) or format string (logrus or `fmt.Sprintf`) of the call.

#### `api/v1alpha1`

Linked into: `agent-cra-frr`, `agent-cra-vsr`, `agent-hbn-l2`, `agent-netplan`, `frr-cra`, `frr-exporter`, `kubectl-nnc`, `operator`, `platform-coil`, `platform-metallb`

| Level | Message | Fields |
|---|---|---|
| info | `validate create` | `name` |
| info | `validate delete` | `name` |
| info | `validate update` | `name` |

#### `api/v1alpha1/network-connector`

Linked into: `network-sync`, `operator`, `platform-coil`, `platform-metallb`

| Level | Message | Fields |
|---|---|---|
| info | `validate create` | `name` |
| info | `validate delete` | `name` |
| info | `validate update` | `name` |

#### `cmd/agent-cra-frr`

Linked into: `agent-cra-frr`

| Level | Message | Fields |
|---|---|---|
| error | `Failed to write response` |  |
| error | `problem running manager` |  |
| error | `unable to create CRA manager` |  |
| error | `unable to initialize components` |  |
| error | `unable to start manager` |  |
| error | `unable to update manager options` |  |
| info | `initialize collectors` |  |
| info | `registered collector` | `collector` |
| info | `starting manager` |  |

#### `cmd/agent-cra-vsr`

Linked into: `agent-cra-vsr`

| Level | Message | Fields |
|---|---|---|
| error | `Failed to write response` |  |
| error | `problem running manager` |  |
| error | `unable to create VSR manager` |  |
| error | `unable to initialize components` |  |
| error | `unable to start manager` |  |
| error | `unable to update manager options` |  |
| info | `initialize collectors` |  |
| info | `registered collector` | `collector` |
| info | `starting manager` |  |

#### `cmd/agent-hbn-l2`

Linked into: `agent-hbn-l2`

| Level | Message | Fields |
|---|---|---|
| error | `problem running manager` |  |
| error | `unable to initialize components` |  |
| error | `unable to start manager` |  |
| info | `starting manager` |  |

#### `cmd/agent-netplan`

Linked into: `agent-netplan`

| Level | Message | Fields |
|---|---|---|
| error | `problem running manager` |  |
| error | `unable to initialize components` |  |
| error | `unable to start manager` |  |
| info | `starting manager` |  |

#### `cmd/frr-cra`

Linked into: `frr-cra`

| Level | Message | Fields |
|---|---|---|
| error | `failed to create listener` |  |
| error | `failed to load base config` |  |
| error | `failed to parse URL` |  |
| error | `failed to parse request` |  |
| error | `failed to read CA certificate` |  |
| error | `failed to read command request body` |  |
| error | `failed to reconcile netlink` |  |
| error | `failed to reconcile policy routes` |  |
| error | `failed to reload FRR, trying to restart` |  |
| error | `failed to restart FRR` |  |
| error | `failed to setup Prometheus registry` |  |
| error | `failed to setup TLS` |  |
| error | `failed to start server` |  |
| error | `failed to write FRR config` |  |
| error | `failed to write command response` |  |
| error | `invalid --ip` |  |
| error | `neighborsync: bridge not found` | `bridge` |
| error | `neighborsync: failed to ensure neighbor suppression` | `bridge` |
| error | `neighborsync: vlan interface not found` | `interface` |
| error | `policy-routes: cannot resolve VRF table` | `vrf` |
| error | `policy-routes: failed to delete stale rule` | `rule` |
| info | `BPF and neighbor sync initialized` |  |
| info | `all L3VNIs registered in FRR` | `count` |
| info | `bringing up VRF interfaces` | `vrf` |
| info | `command request body is empty` |  |
| info | `creating VRF` | `vrf` |
| info | `failed to initialize BPF, neighbor sync disabled` | `error` |
| info | `failed to reconcile mirror configuration, continuing` | `error` |
| info | `policy-routes: added rule` | `rule` |
| info | `reloaded FRR config` |  |
| info | `timed out waiting for L3VNIs in FRR` | `needed` |

#### `cmd/frr-exporter`

Linked into: `frr-exporter`

| Level | Message | Fields |
|---|---|---|
| error | `error configuring monitoring endpoint` |  |
| error | `failed to start server` |  |
| error | `prometheus registry setup error` |  |
| info | `configured Prometheus registry` |  |
| info | `configured monitoring endpoint` |  |
| info | `created server, starting...` | `Addr`, `ReadHeaderTimeout`, `ReadTimeout` |
| info | `loaded kubernetes config` |  |
| info | `loaded status service config` |  |

#### `cmd/network-sync`

Linked into: `network-sync`

| Level | Message | Fields |
|---|---|---|
| error | `problem running manager` |  |
| error | `unable to create controller` | `controller` |
| error | `unable to set up health check` |  |
| error | `unable to set up ready check` |  |
| error | `unable to start manager` |  |
| info | `starting network-sync controller` |  |

#### `cmd/operator`

Linked into: `operator`

| Level | Message | Fields |
|---|---|---|
| error | `failed to setup add Rotator` |  |
| error | `invalid webhook address` |  |
| error | `manager error` |  |
| error | `unable to set up health check` |  |
| error | `unable to set up ready check` |  |
| error | `unable to set up webhook cert readiness check` |  |
| error | `unable to setup reconcilers` |  |
| error | `unable to start manager` |  |
| info | `intent reconciler enabled — legacy ConfigReconciler disabled` |  |
| info | `setting up cert rotation` |  |
| info | `starting manager` |  |

#### `cmd/platform-coil`

Linked into: `platform-coil`

| Level | Message | Fields |
|---|---|---|
| error | `problem running manager` |  |
| error | `unable to create controller` | `controller` |
| error | `unable to set up health check` |  |
| error | `unable to set up ready check` |  |
| error | `unable to start manager` |  |
| info | `starting Coil platform controller` |  |

#### `cmd/platform-metallb`

Linked into: `platform-metallb`

| Level | Message | Fields |
|---|---|---|
| error | `problem running manager` |  |
| error | `unable to create controller` | `controller` |
| error | `unable to set up health check` |  |
| error | `unable to set up ready check` |  |
| error | `unable to start manager` |  |
| info | `starting MetalLB platform controller` |  |

#### `controllers/intent`

Linked into: `operator`

| Level | Message | Fields |
|---|---|---|
| debug | `intent CRD change detected, triggering debounced reconciliation` |  |

#### `controllers/platform`

Linked into: `operator`, `platform-coil`, `platform-metallb`

| Level | Message | Fields |
|---|---|---|
| error | `error listing outbounds for destination mapping` |  |
| error | `error listing podnetworks for network mapping` |  |
| error | `error parsing destination selector` | `outbound` |
| error | `error reconciling node config` | `node` |
| info | `Calico IPPool CRD not present during deletion, nothing to clean up` | `podnetwork` |
| info | `Calico IPPool CRD not yet available, will retry` | `gvk` |
| info | `Outbound has no allocated addresses yet, deferring Coil Egress reconciliation` | `name`, `namespace` |
| info | `creating Calico IPPool` | `cidr`, `name` |
| info | `creating Calico IPPool for PodNetwork` | `cidr`, `name`, `podnetwork` |
| info | `creating Coil Egress` | `name`, `namespace` |
| info | `creating MetalLB resource` | `kind`, `name` |
| info | `creating NodeNetplanConfig` | `node` |
| info | `creating egress NetworkPolicy` | `name` |
| info | `deleted Calico IPPool` | `name` |
| info | `deleted Calico IPPool for PodNetwork` | `name`, `podnetwork` |
| info | `deleted Coil Egress` | `name` |
| info | `deleted IPAddressPool` | `name` |
| info | `deleted L2Advertisement` | `name` |
| info | `deleted NodeNetplanConfig` | `node` |
| info | `deleted egress NetworkPolicy` | `name` |
| info | `deleted stale Calico IPPool` | `name` |
| info | `deleted stale Calico IPPool for PodNetwork` | `name`, `podnetwork` |
| info | `target CRD not yet available, will retry` | `gvk` |
| info | `updating MetalLB resource` | `kind`, `name` |

#### `controllers/shared`

Linked into: `agent-cra-frr`, `agent-cra-vsr`, `agent-hbn-l2`, `agent-netplan`

| Level | Message | Fields |
|---|---|---|
| debug | `NODE_NAME env not set, all events will be rejected` |  |

#### `controllers/sync`

Linked into: `network-sync`

| Level | Message | Fields |
|---|---|---|
| error | `IPAM allocation failed` |  |
| info | `BGPPeering authSecretRef target Secret missing; skipping` | `name`, `namespace` |
| info | `Cluster deleted, removing remote client` |  |
| info | `Deleting remote object` | `kind`, `name` |
| info | `Remote client ready` | `cluster` |
| info | `Remote cluster gone; releasing finalizer without remote delete` | `kind`, `name` |
| info | `Sweeping orphan synced Secret on workload cluster` | `name`, `namespace` |
| info | `kubeconfig Secret has empty 'value' key` | `secret` |
| info | `kubeconfig Secret missing 'value' key` | `secret` |
| info | `kubeconfig Secret not found yet, waiting` | `secret` |
| debug | `Mirrored workload status back to management` | `caughtUp`, `kind`, `name` |
| debug | `Syncing to remote` | `kind`, `name` |

#### `pkg/bpf`

Linked into: `frr-cra`

| Level | Message | Fields |
|---|---|---|
| error | `TCX link Info() failed` | `index`, `interface` |
| error | `failed to detach XDP program` | `index`, `interface` |
| info | `TCX ingress attached` | `index`, `interface` |
| info | `detaching XDP program to avoid conflicts with TCX program` | `index`, `interface` |
| debug | `TCX already attached, skipping` | `index`, `interface` |
| debug | `TCX link verified` | `index`, `interface`, `linkID`, `progID`, `type` |
| debug | `attaching TCX ingress` | `index`, `interface` |

#### `pkg/cra-vsr`

Linked into: `agent-cra-frr`, `agent-cra-vsr`, `frr-cra`, `frr-exporter`

| Level | Message | Fields |
|---|---|---|
| error | `KPI metrics request failed, trying next client` | `url` |
| error | `netconf SSH dial failed, trying next URL` | `url` |
| info | `DisableSegmentation requested, but this feature is unsupported on this node` | `vlanID` |

#### `pkg/debounce`

Linked into: `agent-cra-frr`, `agent-cra-vsr`, `operator`

| Level | Message | Fields |
|---|---|---|
| error | `error debouncing` |  |

#### `pkg/healthcheck`

Linked into: `agent-cra-frr`, `agent-cra-vsr`, `agent-hbn-l2`, `agent-netplan`, `frr-cra`, `frr-exporter`

| Level | Message | Fields |
|---|---|---|
| error | `error while getting node's info` |  |
| error | `error while updating node` |  |
| error | `problem with network interface` | `interface` |
| info | `glob pattern matched no interfaces` | `pattern` |
| info | `node object was modified by another process, will retry taint removal on next reconciliation` |  |
| info | `unable to parse TCP dialer timeout provided in HealtCheck config as duration` | `timeout`, `value` |
| info | `unable to parse TCP dialer timeout provided in HealtCheck config as integer, will use default Timeout` | `timeout`, `value` |

#### `pkg/monitoring`

Linked into: `agent-cra-frr`, `agent-cra-vsr`, `frr-cra`, `frr-exporter`

| Level | Message | Fields |
|---|---|---|
| error | `can't get bgpNeighbors from frr: %w` |  |
| error | `can't get routes from frr` |  |
| error | `can't get vrfs from frr` |  |
| error | `cannot get neighbors from netlink` |  |
| error | `cannot get routes from netlink` |  |
| error | `collector failed` | `duration_seconds`, `name` |
| error | `collector returned no data` | `duration_seconds`, `name` |
| error | `error adding nodename` |  |
| error | `error getting addresses` |  |
| error | `error getting service` |  |
| error | `error listing addresses: no addresses found` |  |
| error | `error preparing ShowBGP command` |  |
| error | `error querying endpoint` |  |
| error | `error validating value` |  |
| error | `protocol not supported` |  |
| error | `request type not supported` |  |
| error | `unable to add nodename` |  |
| error | `unable to set input` |  |
| error | `unable to set longer prefixes` |  |
| info | `collector succeeded` | `duration_seconds`, `name` |
| info | `command to be executed` | `command` |
| info | `created ServeMux` |  |
| info | `got QueryAll request` |  |
| info | `got ShowBGP request` |  |
| info | `got ShowBGPSummary request` |  |
| info | `got ShowEVPN request` |  |
| info | `got ShowRoute request` |  |
| info | `response written` | `type` |
| info | `will querry endpoints` | `endpoints` |

#### `pkg/neighborsync`

Linked into: `frr-cra`

| Level | Message | Fields |
|---|---|---|
| error | `failed to create ARP packet` | `address`, `interface` |
| error | `failed to create gratuitous ARP packet` | `address`, `interface` |
| error | `failed to dial ARP` | `interface` |
| error | `failed to get IPv4 address from interface` | `interface` |
| error | `failed to get interface by index` | `linkIndex` |
| error | `failed to list neighbors` | `linkIndex` |
| error | `failed to list neighbors on bridge, assuming MAC changed` | `bridge` |
| error | `failed to listen for NDP` | `interface` |
| error | `failed to open NDP socket` | `interface` |
| error | `failed to open ringbuf reader` |  |
| error | `failed to read ringbuf` |  |
| error | `failed to subscribe to neighbor updates` |  |
| error | `neigh replace failed` |  |
| error | `sendARPRequest failed` | `address`, `interface` |
| error | `sendGratuitousARP failed` | `address`, `interface` |
| error | `sendNDPRequest failed` | `address` |
| error | `sendUnsolicitedNA failed` | `address`, `interface` |
| info | `BPF ringbuf reader goroutine started` |  |
| info | `BPF ringbuf reader opened, waiting for events` |  |
| info | `MAC changed, sending gratuitous neighbor` | `bridge`, `ip` |
| info | `neighbor updates channel closed, restarting neighbor sync, clearing timers` |  |
| info | `sendGratuitousNeighbor: unsupported IP address type` | `address` |
| info | `sendNeighborRequest: unsupported IP address type` | `address` |
| debug | `BPF neighbor event` | `family`, `ifindex`, `ip`, `mac`, `skipped` |
| debug | `interface removed, deleting neighbor timer` | `address`, `linkIndex` |
| debug | `neighbor delete` | `flags`, `ip`, `link`, `mac`, `state` |
| debug | `neighbor update` | `flags`, `ip`, `link`, `mac`, `state` |

#### `pkg/network/net`

Linked into: `agent-cra-frr`, `agent-cra-vsr`, `agent-hbn-l2`, `agent-netplan`, `frr-cra`, `frr-exporter`, `kubectl-nnc`, `operator`, `platform-coil`, `platform-metallb`

| Level | Message | Fields |
|---|---|---|
| info | `deleting link %s` |  |
| info | `removing bond %s` |  |
| V(2) | `searching network link %s` |  |

#### `pkg/network/netplan`

Linked into: `agent-cra-frr`, `agent-cra-vsr`, `agent-hbn-l2`, `agent-netplan`, `frr-cra`, `frr-exporter`, `kubectl-nnc`, `operator`, `platform-coil`, `platform-metallb`

| Level | Message | Fields |
|---|---|---|
| info | `virtual interface %s was removed` |  |

#### `pkg/network/netplan/client/dbus`

Linked into: `agent-netplan`

| Level | Message | Fields |
|---|---|---|
| error | `error authenticating with dbus. err: %s` |  |
| error | `error dialing connection with dbus using %s. err: %s` |  |
| error | `error sending Hello message to dbus. err: %s` |  |
| info | `applying new netplan configuration` |  |
| info | `cleaning existing configuration` |  |
| info | `dialing with dbus using address %s` |  |
| info | `error deleting %s link %s. err: %s` |  |
| info | `netplan apply was successful` |  |
| info | `removing links for virtual interfaces removed from configuration before netplan apply` |  |
| V(2) | `applying configuration` |  |
| V(2) | `error applying configuration. err: %s` |  |
| V(2) | `setting netplan path %s for hint %s` |  |
| V(2) | `trying configuration for %d seconds` |  |

#### `pkg/network/netplan/client/direct`

Linked into: `agent-netplan`

| Level | Message | Fields |
|---|---|---|
| error | `failed to write input into stdin: %s` |  |
| info | `netplan output` | `output` |

#### `pkg/network/netplan/client/dummy`

Linked into: `agent-netplan`

| Level | Message | Fields |
|---|---|---|
| info | `cleaning existing configuration` |  |
| info | `clearing temp dir used by dummy netplan client: %s` |  |
| info | `creating dummy netplan client using directory %s` |  |
| info | `error parsing netplan file %s; moving on. err: %s` |  |
| info | `failed to remove %s: %s` |  |
| info | `netplan configuration has changed. applying new state` |  |
| info | `removing existing netplan file %s` |  |
| info | `removing links for virtual interfaces removed from configuration before netplan apply` |  |
| info | `would remove link %s if not dummy` |  |

#### `pkg/reconciler/agent-hbn-l2`

Linked into: `agent-hbn-l2`

| Level | Message | Fields |
|---|---|---|
| error | `failed to update network operator readiness condition` |  |

#### `pkg/reconciler/agent-netplan`

Linked into: `agent-netplan`

| Level | Message | Fields |
|---|---|---|
| error | `failed to discard pending netplan configuration` |  |
| error | `failed to update network operator readiness condition` |  |
| info | `netplan configuration already in sync, skipping apply` |  |

#### `pkg/reconciler/common`

Linked into: `agent-cra-frr`, `agent-cra-vsr`

| Level | Message | Fields |
|---|---|---|
| error | `failed to remove taints from node, will retry on next reconciliation` |  |
| error | `failed to update network operator readiness condition` |  |
| info | `config to reconcile` | `NodeNetworkConfig` |
| info | `invalidating NodeNetworkConfig` | `error`, `name`, `reason` |
| info | `restored last known valid NodeNetworkConfig` |  |
| info | `restoring previous NodeNetworkConfig` |  |
| info | `setting NodeNetworkConfig status` | `name`, `status` |
| info | `skipping invalid NodeNetworkConfig` | `name` |

#### `pkg/reconciler/intent`

Linked into: `operator`

| Level | Message | Fields |
|---|---|---|
| error | `IPAM allocation failed` |  |
| error | `VRF name reduction failed` | `node` |
| error | `assembly failed` | `node` |
| error | `builder failed` | `builder` |
| error | `failed to apply NodeNetplanConfig` | `node` |
| error | `failed to apply NodeNetworkConfig` | `node` |
| error | `failed to delete orphaned NNC` | `name` |
| error | `failed to delete orphaned NodeNetplanConfig` | `name` |
| error | `failed to set Node owner reference` | `nnc` |
| error | `failed to update Collector status` | `collector` |
| error | `finalizer reconciliation failed` |  |
| error | `legacy conflict detection failed` |  |
| error | `orphaned NNC cleanup failed` |  |
| error | `orphaned NodeNetplanConfig cleanup failed` |  |
| error | `revision hash failed` | `node` |
| error | `status condition update failed` |  |
| error | `subnet allocation failed` | `collector`, `subnet` |
| error | `unable to list NodeNetworkConfigs for local ASN` |  |
| info | `BGPPeering authSecretRef Secret has no 'password' key; peering will have no password` | `bgppeering`, `secret` |
| info | `BGPPeering authSecretRef not resolvable; peering will have no password` | `bgppeering`, `error`, `secret` |
| info | `NNC update conflict, retrying` | `attempt`, `node` |
| info | `deleting orphaned NodeNetplanConfig` | `name` |
| info | `deleting orphaned NodeNetworkConfig` | `name` |
| info | `intent reconciliation complete` |  |
| info | `legacy CRDs detected — reconciliation continues` | `conflicts` |
| info | `skipping NodeNetworkConfig apply due to builder failure; preserving last-good config` |  |
| info | `skipping node — currently provisioning` | `node` |
| info | `starting intent reconciliation` |  |
| info | `stripping legacy owner reference` | `nnc`, `ownerKind`, `ownerName` |

#### `pkg/reconciler/intent/builder`

Linked into: `operator`

| Level | Message | Fields |
|---|---|---|
| info | `skipping BGPPeering with unknown mode` | `bgppeering`, `mode` |
| info | `skipping BGPPeering with unresolvable listenRange` | `bgppeering`, `error` |
| info | `skipping Collector with unknown VRF reference` | `collector`, `vrf` |
| info | `skipping Inbound with ambiguous announcement policy` | `error`, `inbound` |
| info | `skipping Inbound with unknown Network reference` | `inbound`, `networkRef` |
| info | `skipping Layer2Attachment` | `error`, `l2a` |
| info | `skipping Layer2Attachment with unknown Network reference` | `l2a`, `networkRef` |
| info | `skipping Layer2Attachment with unresolvable destinations` | `error`, `l2a` |
| info | `skipping NodeAttachment with invalid node selector` | `error`, `nodeattachment` |
| info | `skipping Outbound with ambiguous announcement policy` | `error`, `outbound` |
| info | `skipping PodNetwork with ambiguous announcement policy` | `error`, `podnetwork` |
| info | `skipping PodNetwork with unknown Network reference` | `networkRef`, `podnetwork` |
| info | `skipping PodNetwork with unresolvable destinations` | `error`, `podnetwork` |
| info | `skipping TrafficMirror with unknown source kind` | `kind`, `trafficmirror` |
| info | `skipping TrafficMirror with unresolvable collector` | `error`, `trafficmirror` |
| info | `skipping TrafficMirror with unresolvable source` | `error`, `trafficmirror` |

#### `pkg/reconciler/intent/finalizer`

Linked into: `operator`

| Level | Message | Fields |
|---|---|---|
| debug | `added collector-in-use finalizer` | `collector` |
| debug | `added destination-in-use finalizer` | `destination` |
| debug | `added network-in-use finalizer` | `network` |
| debug | `added vrf-in-use finalizer` | `vrf` |
| debug | `removed collector-in-use finalizer` | `collector` |
| debug | `removed destination-in-use finalizer` | `destination` |
| debug | `removed network-in-use finalizer` | `network` |
| debug | `removed vrf-in-use finalizer` | `vrf` |

#### `pkg/reconciler/intent/ipam`

Linked into: `network-sync`, `operator`

| Level | Message | Fields |
|---|---|---|
| error | `IPAM allocation failed for Inbound` | `inbound` |
| error | `IPAM allocation failed for Outbound` | `outbound` |
| error | `IPAM allocation partially failed for Inbound` | `inbound` |
| error | `IPAM allocation partially failed for Outbound` | `outbound` |
| error | `node IP allocation failed for Layer2Attachment` | `l2a` |
| error | `node IP allocation partially failed` | `l2a`, `node` |
| info | `allocated addresses for Inbound` | `addresses`, `inbound` |
| info | `allocated addresses for Outbound` | `addresses`, `outbound` |
| info | `allocated node IP` | `addresses`, `l2a`, `node` |

#### `pkg/reconciler/intent/legacy`

Linked into: `operator`

| Level | Message | Fields |
|---|---|---|
| info | `legacy CRD conflicts detected — consider migrating to intent CRDs` | `conflicts`, `count` |

#### `pkg/reconciler/intent/status`

Linked into: `operator`

| Level | Message | Fields |
|---|---|---|
| info | `nodes report differing local ASNs for BGPPeering; leaving asNumber unset` | `asn`, `bgppeering`, `conflictingASN`, `node` |

#### `pkg/reconciler/operator`

Linked into: `agent-cra-frr`, `agent-cra-vsr`, `operator`

| Level | Message | Fields |
|---|---|---|
| error | `error getting list of BGP peering configurations from Kubernetes` |  |
| error | `error getting list of Layer2s from Kubernetes` |  |
| error | `error getting list of MirrorSelectors from Kubernetes` |  |
| error | `error getting list of MirrorTargets from Kubernetes` |  |
| error | `error getting list of VRFs from Kubernetes` |  |
| error | `node configuration failed` | `configHash`, `node` |
| error | `revision deployment aborted` |  |
| info | `NetworkConfigRevision creation aborted - new revision equals to the last known one` |  |
| info | `NetworkConfigRevision creation aborted - new revision equals to the last known valid one` |  |
| info | `NetworkConfigRevision creation aborted - new revision is equal to known invalid revision` |  |
| info | `creating NetworkConfigRevision` | `name` |
| info | `deleting NetworkConfigRevision` | `name` |
| info | `deleting redundant NodeNetworkConfig` | `name` |
| info | `deployed NetworkConfigRevision` | `name` |
| info | `deployed NodeNetworkConfig` | `name` |
| info | `fetching config data...` |  |
| info | `invalidating revision` | `failedMessage`, `failedNode`, `name` |
| info | `new NetworkConfigRevision prepared` | `name` |

<!-- END GENERATED -->
