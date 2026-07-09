---
title: Debugging
description: >-
  Inspect the low-level per-node resources the operator generates from intent to
  find out why intent is not applied to the dataplane.
---

# Debugging

When you author an intent resource in the
`network-connector.sylvaproject.org` API group, the operator does not push it to
nodes directly. It compiles all intent (and any legacy low-level resources) into
a cluster-wide **`NetworkConfigRevision`**, then rolls that revision out
**node by node** by generating per-node **`NodeNetworkConfig`** and
**`NodeNetplanConfig`** objects. The node agents read those objects and program
FRR/vSR and the host network namespace.

```text
intent CRDs ──▶ NetworkConfigRevision ──▶ NodeNetworkConfig  ──▶ agent-cra-frr / agent-cra-vsr
(network-connector)   (revision + rollout)   NodeNetplanConfig ──▶ agent-netplan / agent-hbn-l2
```

To debug why intent is not reflected on a node, work **top-down**: start at your
intent resource's status, follow it to the revision and its rollout counters,
then to the affected node's `NodeNetworkConfig`, and finally compare against the
actual interfaces/routes reported in `NodeNetworkStatus`.

!!! note
    The intent controllers do **not** generate intermediate low-level
    `VRFRouteConfiguration` / `Layer2NetworkConfiguration` / `BGPPeering`
    objects. Intent is merged straight into the revision. The low-level API
    remains available only as a manual escape hatch — see
    [Legacy API](legacy-api.md).

## The generated resources

All per-node resources except `NodeNetworkStatus` live in the
`network.t-caas.telekom.com/v1alpha1` group. `NodeNetworkStatus` lives in the
`network-connector.sylvaproject.org/v1alpha1` group. All are cluster-scoped and
named after the node (except `NetworkConfigRevision`, which is named by hash).

| Resource | Short name | What it is | Consumed / populated by |
|----------|-----------|------------|-------------------------|
| `NodeNetworkConfig` | `nnc` | Resolved per-node FRR/VXLAN/VRF/BGP config for one node | `agent-cra-frr`, `agent-cra-vsr` |
| `NodeNetplanConfig` | _(none)_ | Desired host interface state for one node | `agent-netplan`, `agent-hbn-l2` |
| `NetworkConfigRevision` | `ncr` | Cluster-wide snapshot of all config plus rollout status | operator (rollout controller) |
| `NodeNetworkStatus` | `nns` | Per-node interface and route inventory (observed state) | populated by the CRA agents |

## Tracing intent to the dataplane

### 1. Check your intent resource's status

Intent resources carry conditions. Look for `Ready`, `Resolved` and `Applied`;
failures usually surface as `InterfaceNotFound` or `DuplicateVRF`.

```bash
kubectl describe layer2attachment <name>
kubectl get layer2attachment <name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
```

See [Common conditions](#common-conditions) for what each type means.

### 2. Find the revision and its rollout status

Every accepted config change produces a new `NetworkConfigRevision`. The rollout
counters tell you how far the revision has progressed across the cluster.

```bash
kubectl get ncr
```

The printed columns are `Invalid`, `Queued`, `Ongoing`, `Ready`, `Total` and
(at higher verbosity) `FailedNode`:

```bash
kubectl get ncr -o wide
kubectl get ncr <revision> \
  -o jsonpath='ready={.status.ready} ongoing={.status.ongoing} queued={.status.queued} total={.status.total}{"\n"}'
```

- `ready` — nodes already provisioned with this revision.
- `ongoing` — nodes currently being provisioned.
- `queued` — nodes still waiting for their turn.
- `total` — nodes targeted by this revision.
- `isInvalid` / `failedNode` — set when a node failed to provision (see
  [Rollout troubleshooting](#rollout-troubleshooting)).

### 3. Inspect the NodeNetworkConfig on the affected node

The `NodeNetworkConfig` is named after the node. Its `status.configStatus` is one
of `provisioning`, `provisioned` or `invalid`.

```bash
kubectl get nnc <node-name>
kubectl get nnc <node-name> -o jsonpath='{.status.configStatus}{"\n"}'

# Compare the config the node was built from against the last one it applied.
kubectl get nnc <node-name> -o jsonpath='spec.revision={.spec.revision}{"\n"}lastApplied={.status.lastAppliedRevision}{"\n"}'

# When configStatus is "invalid", the reason is here:
kubectl get nnc <node-name> -o jsonpath='{.status.errorMessage}{"\n"}'
```

`kubectl describe nnc <node-name>` shows the full resolved spec —
`layer2s`, `clusterVRF`, `fabricVRFs` and `localVRFs` — so you can confirm the
VNIs, route targets, BGP peers and static/policy routes the operator derived from
your intent. The `status.asNumber` field reports the local (platform-side) BGP
AS number the node agent is configured with.

### 4. Inspect the actual interfaces and routes

`NodeNetworkStatus` (short name `nns`) is populated by the agents with the
observed interface and route inventory. Use it to confirm whether the desired
config actually landed on the host.

```bash
kubectl get nns <node-name>
kubectl get nns <node-name> -o jsonpath='{range .status.interfaces[*]}{.name} {.state} {.addresses}{"\n"}{end}'
kubectl get nns <node-name> -o jsonpath='{range .status.routes[*]}{.destination} via {.gateway} dev {.interface} table {.table}{"\n"}{end}'
```

Each interface entry reports `name`, `state` (`up`/`down`/`unknown`), `type`,
`mtu`, `mac`, `addresses`, and (where relevant) `parent`, `vlanID` or `members`.

## The kubectl-nnc plugin

`kubectl-nnc` is a plugin for inspecting `NodeNetworkConfig` resources with a
tree/table rendering that is easier to read than raw YAML.

Build it with the Makefile target (produces `bin/kubectl-nnc`), then put it on
your `PATH` so `kubectl` discovers it as `kubectl nnc`:

```bash
make kubectl-nnc
sudo install bin/kubectl-nnc /usr/local/bin/kubectl-nnc
```

Subcommands:

| Command | Description |
|---------|-------------|
| `kubectl nnc list` | List all `NodeNetworkConfig`s with a summary. |
| `kubectl nnc show <node-name>` | Show the detailed `NodeNetworkConfig` for one node. |

Persistent flags apply to both subcommands:

- `--kubeconfig <path>` — path to the kubeconfig file.
- `--context <name>` — kubeconfig context to use.
- `--no-color` — disable colored output.

```bash
kubectl nnc list
kubectl nnc show <node-name> --context my-cluster
```

## Rollout troubleshooting

The rollout is gated: the operator provisions one node, waits for its
`NodeNetworkConfig` to reach `configStatus: provisioned`, then moves on. A single
failing node stalls the rollout for the whole revision.

- **Stuck rollout / invalidated revision.** If a node fails to provision, the
  revision is marked invalid and records the culprit:

  ```bash
  kubectl get ncr <revision> \
    -o jsonpath='invalid={.status.isInvalid} node={.status.failedNode}{"\n"}{.status.failedMessage}{"\n"}'
  ```

  `status.failedAt` records when the failure happened. Inspect that node's
  `nnc` `status.errorMessage` for the underlying cause.

- **A node not progressing.** Check that node's `nnc.status.configStatus`. If it
  stays `provisioning`, the agent on that node is not applying the config —
  inspect the `agent-cra-frr` / `agent-cra-vsr` (or `agent-netplan` /
  `agent-hbn-l2`) pod logs on that node.

- **Revision mismatch.** If `nnc.status.lastAppliedRevision` does not match
  `nnc.spec.revision`, the node has not yet applied the latest build. A
  persistent mismatch points to an agent that is failing to apply or is not
  running on that node.

## Common conditions

Intent resources in the `network-connector.sylvaproject.org` group expose these
condition types in `status.conditions`:

| Condition | Meaning |
|-----------|---------|
| `Ready` | The resource was successfully reconciled. |
| `Resolved` | All references (`networkRef`, `vrfRef`, etc.) were resolved. |
| `Applied` | Configuration was applied to the target nodes. |
| `InterfaceNotFound` | A referenced interface does not exist on a target node. |
| `DuplicateVRF` | Another VRF object in the same namespace declares the same `spec.vrf`, causing a conflict. |

Read them per resource with `kubectl describe <kind> <name>` and follow any
failure down through the revision and `nnc` as described above.

## Node readiness

Whether a node is considered ready is signalled separately through the
`NetworkOperatorReady` node condition and taint removal. See
[Node Readiness](../reference/node-readiness.md).

## Related

- [Metrics](../reference/metrics.md) — operator and rollout metrics.
- [CRD Reference](../reference/crd-reference.md) — full field reference.
- [Legacy API](legacy-api.md) — the low-level `network.t-caas.telekom.com` CRDs.
