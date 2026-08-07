# cni-workload — routed, no-shared-L2 CNI for KubeVirt VMs (and routed pods)

`cni-workload` gives a workload (a KubeVirt VM, or later a routed pod) a **fully
routed** secondary interface with **no shared L2**: the workload gets a real
routable IPv4 `/32` + IPv6 `/128`, and the CRA-side veth end is moved into the
CRA network namespace where the routing daemon (FRR / 6WIND VSR) advertises
on-link host routes to it via BGP.

## How it works

Multus invokes the plugin for a secondary network. On `ADD` the plugin:

1. Delegates to the configured **IPAM** (`static`, `host-local` or `whereabouts`) to obtain the
   workload's `/32` + `/128`.
2. Creates a **veth pair** in the pod netns. The pod-side end keeps the IPAM
   addresses and is the Multus interface (KubeVirt's built-in `bridge` binding
   enslaves it to a private, per-pod 2-port bridge together with the qemu tap —
   this is the only L2 and it is **not** a shared broadcast domain).
3. Moves the **peer end** into the CRA network namespace (see *netns discovery*),
   names it `cra<sha256(containerID + "/" + ifName)[:12]>` (12 hex characters,
   filling the 15-character kernel limit), sets its ifalias to
   `infra-<portname>` (required by VSR, ignored by FRR) and brings it up.
4. Hands the attachment to the **node-local CRA agent** over the gRPC unix
   socket. The agent records it in the node's `NodeWorkloadPorts` object and does
   *all* L3 programming — the on-link **link-local gateway** addresses the
   workload uses as its next-hop (`169.254.1.1/32`, `fe80::1/128` by default;
   each only for an address family the IPAM result contains) and the **on-link
   host routes** (`<ip>/32`, `<ip>/128`) for the workload's addresses.

The plugin is therefore **flavor-agnostic**: it only wires the veth. `DEL`
tells the agent to drop the attachment first, then removes the veth (removing
one end removes both), and releases the IPAM allocation last, only once every
step before it succeeded: a freed address must not be re-used while its host
routes are still exported for the stale port or while the kernel still routes
it toward a lingering veth; the retried `DEL` releases it. A failed `ADD` rolls back
under the same rule: the compensating drop runs first, and only if the agent
confirms it is the allocation released — otherwise the `ADD` error reports the
lingering entry and the allocation is left for the runtime's `DEL` to reclaim.

The agent's "dropped" acknowledgement means the attachment is gone from the
durable `NodeWorkloadPorts` object; the datapath withdrawal (host routes, BGP
advertisement) follows asynchronously from the reconciler. An address may
therefore be re-allocated a short moment before its old routes vanish — a window
the same length as one reconcile round trip. Deployments that cannot tolerate
this should size the IPAM pool so that freed addresses are not immediately
re-used (`host-local` hands out the lowest free address, so this mostly
matters for very small pools).

Every address in the IPAM result is narrowed to a host prefix (`/32`, `/128`)
before it is assigned to the pod interface and returned to the runtime: the
attachment is routed through the on-link gateway, and a pool-sized prefix would
install a connected route on the isolated veth instead.

### VRF vs underlay

- **`vrf` omitted / `default` / `main` → UNDERLAY.** The CRA-side port is left in
  the CRA netns default routing table (`RT_TABLE_MAIN`). The fabric/underlay BGP
  session redistributes the on-link `/32` + `/128` toward the leaf/DCGW. This is
  the datapath test target: reach the VM/pod IP from a leaf/DCGW **via the
  underlay**, not the EVPN overlay.
- **`vrf: <name>` → tenant VRF.** The port is enslaved to that VRF device and the
  routes are programmed in the VRF's table (exported e.g. as EVPN type-5).
  Names of platform-owned VRFs (the CRA base config's management and cluster
  VRF) are rejected at `ADD` and, should they still reach the node's
  `NodeWorkloadPorts`, dropped by the agent before reconciliation — a workload
  port must never turn a platform VRF into a workload-managed local VRF. The
  agent applies the same validation (single-address gateways and host routes
  of the right family) to every recorded entry, so a hand-written object cannot
  smuggle a subnet route into the fabric either.

## Configuration

Delivered per secondary network via a `NetworkAttachmentDefinition`
(`spec.config`). Fields:

| field               | required | default | description |
| ------------------- | -------- | ------- | ----------- |
| `type`              | yes      | —       | must be `cni-workload` |
| `ipam`              | yes      | —       | delegated IPAM block; `type` must be one of `static`, `host-local`, `whereabouts` (the plugin executes that binary as root, so other chained plugins are refused, and node-level IPAM options — `host-local` `dataDir`/`resolvConf`, `whereabouts` `configuration_path`/`log_file`/`kubernetes`/`datastore`/`etcd_*` — are refused too, see [Who may author a NetworkAttachmentDefinition](#who-may-author-a-networkattachmentdefinition)) |
| `vrf`               | no       | *(underlay)* | CRA VRF device name; omit/`default`/`main` for the underlay/default table; management/cluster VRF names are rejected |
| `linkLocalGateways` | no       | `169.254.1.1` / `fe80::1` | on-link next-hop addresses the agent configures on the CRA-side port; must be link-local unicast (`169.254.0.0/16`, `fe80::/10`) — the plugin and the agent both reject anything else |
| `mtu`               | no       | `1500`  | veth MTU |

Example (underlay, static IPAM) — see
[`e2e/kubevirt/manifests/networkattachmentdefinition.yaml`](../../e2e/kubevirt/manifests/networkattachmentdefinition.yaml).

### Who may author a NetworkAttachmentDefinition

The NAD decides *which network* a port joins: the VRF and, through the
delegated IPAM block, the host addresses the CRA advertises for the workload.
Those are network-intent decisions on the same level as a VRF or
`Layer2Attachment` object, and the agent treats them as such: it does not
check IPAM results against per-network address pools or verify that the VRF
"belongs" to the NAD's namespace, so a `cni-workload` NAD grants its pods
whatever the NAD says. Restrict `create`/`update` on
`network-attachment-definitions` to the team that owns the network intents
(or to a policy engine that pins `ipam` and `vrf` per namespace); do not hand
it to tenants who only run workloads. What the agent *does* enforce is the
platform's own integrity: the management and cluster VRFs are refused,
gateways must be link-local, and `hostRoutes` are host prefixes only, so a
mis-authored NAD cannot take over the cluster or management plane. The
settings that decide *where on the node* the root-run plugin wires a port are
excluded from the NAD altogether (next section). The same applies to the
delegated `ipam` block: it is handed verbatim to the root-run IPAM binary on
ADD and DEL, so besides restricting `ipam.type` to actual address managers the
plugin refuses the IPAM options that select host paths or credentials (in any
casing, since the IPAM plugins decode their config case-insensitively) —
`dataDir` and `resolvConf` for `host-local`; `configuration_path`, `log_file`,
`kubernetes`, `datastore` and the `etcd_*` settings for `whereabouts`. Those
come from the IPAM's compiled-in defaults or its node-level config file, so a
NAD cannot point the allocation store at an arbitrary host directory, read an
arbitrary host file as resolv.conf, or use a foreign kubeconfig.

### Node config (trust anchors)

The settings that decide *where* the root-run plugin wires a port are node
properties and are **not accepted from the `NetworkAttachmentDefinition`**
(which is tenant-writable in a multi-tenant cluster — an ADD carrying one of
them fails). They live in the root-owned node config
`/etc/cni/cni-workload/config.json`, which the installer DaemonSet writes from
the `cni-workload-node-config` ConfigMap
([`config/cni-workload/node-config.yaml`](../../config/cni-workload/node-config.yaml));
the plugin refuses the file when it is not a regular, root-owned file without
group/world write permission. All keys are optional:

| key              | default | description |
| ---------------- | ------- | ----------- |
| `agentSocket`    | `/run/das-schiff/workload-cni.sock` | unix socket of the node-local CRA agent |
| `craNetns`       | `auto`  | `auto` (discover by trunk), a named netns under `/var/run/netns/<name>`, or an absolute path (e.g. `/proc/<pid>/ns/net`); a name/path is only accepted if it owns the peer of `trunkInterface` |
| `trunkInterface` | `hbn`   | interface that identifies the CRA netns during auto-discovery; mirrors `BaseConfig.trunkInterfaceName` |

## netns discovery

The CRA netns is resolved with this precedence
(`pkg/cni/discovery.go`):

1. an absolute `craNetns` path;
2. a named `craNetns` under `/var/run/netns/<name>`;
3. `auto`: the namespace on the far end of the `trunkInterface` veth (mirrors
   the cra-vsr `findWorkNSName` heuristic, so a single value drives both CRA
   flavors). Candidates under `/var/run/netns` and `/proc/<pid>/ns/net` are
   matched by the kernel-reported peer netns id and peer ifindex, **not** by
   interface name — a pod that merely names one of its interfaces `hbn` (which
   any Multus annotation can do) is never mistaken for the CRA netns.

In every case the host-side trunk must be a veth whose peer sits in the CRA
netns, and an explicit path or name is accepted only if that namespace owns the
peer: `craNetns` can skip the discovery scan but cannot redirect the CRA-side
veth into the host netns or another workload's netns. The trunk name itself is
the trust anchor of that check, which is why it — like `craNetns` and the agent
socket — is read from the operator-owned node config only and never from the
(possibly tenant-controlled) NAD.

The node config's `trunkInterface` has to match the CRA base config's
`trunkInterfaceName` (both default to `hbn`); an overlay that renames the trunk
must set both.

## CRA flavor notes

Both flavors use the same transport: the plugin calls the node-local agent, the
agent records the attachment on `NodeWorkloadPorts` and merges it into the
`NodeNetworkConfig` before rendering. Only the rendering differs. **This
repository state ships the cra-frr rendering only**; the plugin, API and
transport are flavor-agnostic, but the cra-vsr side (agent wiring and NETCONF
rendering) lands in the follow-up #356 — until then an attachment recorded on a
cra-vsr node is not programmed into the datapath.

- **cra-frr:** the agent programs the CRA-FRR netns via netlink
  (`pkg/nl/workloadports.go`): VRF enslavement, the on-link gateway addresses and the
  scope-link host routes. FRR redistributes connected/kernel/static, so the
  `/32` + `/128` are advertised. For the **underlay** path the FRR *default*
  instance must redistribute connected/kernel toward the fabric neighbors (and
  gain an IPv6 unicast address-family).
- **cra-vsr (follow-up #356):** the VSR fast path owns the FIB, so the moved
  port cannot be programmed via raw netlink. The agent will render it as NETCONF
  instead: an `interface infrastructure <ifname>` with `port infra-<ifname>` +
  the on-link gateway addresses, plus interface-static routes
  (`ipv4-route/ipv6-route <ip> next-hop <ifname>`). Underlay (no-VRF) ports also
  get an explicit BGP `network` statement, since the default table's session has
  no VRF redistribution. What this PR already does for it is the flavor-agnostic
  groundwork: the `infra-<ifname>` alias on the moved port (see below) and the
  `pkg/workloadcni` transport.

### Transport

```
CNI ADD/DEL --gRPC(unix)--> agent --> NodeWorkloadPorts CR (durable)
                                   \-> merge into NodeNetworkConfig --> NETCONF
```

The agent serves the socket at `/run/das-schiff/workload-cni.sock` (a hostPath
shared with the plugin, which runs in the host mount namespace). Both the socket
and its directory are root-only (`0600` / `0700`) so no unprivileged local
process can alter routing state. A change to
`NodeWorkloadPorts` triggers a reconcile even when the `NodeNetworkConfig`
revision is unchanged (the merged workload ports are tracked by a content hash),
so attachments are (de)provisioned promptly.

## Build & install

```sh
make build-cni-workload          # host build of bin/cni-workload (Linux)
make docker-build              # also builds das-schiff-nwop-cni-workload:latest
make kind-load                 # loads images into the kind cluster
kubectl apply -k <overlay>       # an overlay that includes the cni-workload component
```

The installer DaemonSet copies the binary to `/opt/cni/bin`. The per-network CNI
config travels with the NAD, so no standalone conflist is required.
