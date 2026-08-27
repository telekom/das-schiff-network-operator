# Network debugging runbooks

This page documents the network-operator side of diagnostic, repair, and node
recovery workflows. A deployment may expose these workflows through generic
DebugSession intents such as `network-diagnostics`, `network-repair`, and
`node-recovery`; the session layer controls approval, target selection,
duration, evidence handling, and cleanup. The network operator reports desired
and observed state. Neither component should infer authorization from a
network name, namespace, node label, or profile name.

## Safety boundary

Use a restricted profile for API and workload observations. Use an elevated
node profile only for an explicitly approved host-network investigation, with
an exact node, bounded duration, bounded evidence volume, and the minimum
Linux capabilities required by the selected command. The profile must not
enable unrestricted `exec`, `privileged: true`, host PID/IPC, or a writable
host path by default. Keep packet captures and traces in the session's
approved evidence directory, redact addresses and payloads before sharing,
and terminate the session when the evidence is collected.

If a deployment supplies a compatible bounded network-debug image, selected-pod
capture remains a session-layer capability rather than a network-operator
capability. Use the approved image/session configuration and do not add a
second ad-hoc privileged DaemonSet for an incident. The optional image
integration is described below; this repository does not require that image.

Platform ADRs, admission controls, and service-integration protections are
deployment policy, not implicit properties of a network resource. A repair
profile must receive an exact node, interface, allowlisted operation, and
confirmation token from that policy layer. A diagnostic profile may inspect a
selected customer-pod network namespace only when the Breakglass controller
explicitly grants that target; it must not discover or join arbitrary pod
namespaces.

## Source-to-runbook traceability

The following table records which implementation or operational source supports
each section and what remains deployment-specific. Deployment-specific cluster
names, addresses, versions, and access paths are intentionally not copied here.

| Runbook area | Source evidence | Generic contract | Deployment-specific boundary |
|---|---|---|---|
| HBN intent and rollout | Operator `docs/advanced/debugging.md`; `NodeNetworkConfig`, `NodeNetplanConfig`, and `NodeNetworkStatus` APIs | Trace intent → revision → per-node desired/applied/observed state; treat stale or `Unknown` evidence as unknown | Resource labels, namespaces, and target cluster are installation inputs |
| BGP/EVPN/FRR | CRA-FRR agent and FRR CLI behavior | Use read-only `vtysh` summaries for BGP, EVPN, and BFD; never persist node CLI changes | VRF names, peer addresses/ASNs, CLI container, and access route |
| Netplan | `NodeNetplanConfig` API and netplan-agent behavior | Compare desired state with observed links/routes and agent logs; plan disruption explicitly | OS/version behavior, change window, and exact downtime are platform facts |
| CRA/VSR | CRA-VSR/HBN-L2 agent interfaces and the selected VSR implementation | Select the actual stack, inspect health/config, and use only an approved node profile for host access | Container runtime namespace, CLI syntax/version, dashboard, and alert names |
| Multus/SR-IOV | `Layer2Attachment`/`InterfaceConfig` APIs and the installed CNI | Verify NAD, resource annotation, pod network-status, node allocatable resource, selectors, and taints independently | CNI implementation, resource names, VLAN/trunk rules, node pools, and sensitive-traffic policy |
| MultiNetworkPolicy | The external policy project's API and daemon behavior | Treat secondary-interface enforcement as an external plane; test allowed/denied flows separately | Daemon deployment, policy API version, NAD annotation, and supported port semantics |
| Pod/host tcpdump | The deployment's approved capture-image contract | Use a bounded capture operation; require explicit namespace/host scope and approved evidence path | Session authorization, target selection, capabilities, and data-retention policy |
| Host `pwru` | The deployment's approved trace-image contract and Linux kernel behavior | Require BTF/tracefs/securityfs and bounded SIGINT/BPF-detach cleanup; no privileged fallback | Kernel features and approved capability policy |
| Route/neighbor/FDB evidence and repair | Linux `ip`/`bridge` behavior and the deployment repair contract | Use `ip route get`, inspect neighbors/FDB, and permit only bounded, confirmed flush actions | Exact destination/interface, allowlist, confirmation token, and admission policy |

## Triage path

Start with the resource that expresses the desired state and follow the
operator pipeline to the node and the dataplane:

```text
intent CRD -> NetworkConfigRevision -> NodeNetworkConfig/NodeNetplanConfig
           -> CRA or netplan agent -> NodeNetworkStatus -> observed traffic
```

Run the following with a read-only Kubernetes identity where possible. Replace
angle-bracket values; do not copy production names, addresses, or credentials
into tickets or examples.

```bash
kubectl describe <intent-kind> <intent-name> -n <intent-namespace>
kubectl get <intent-kind> <intent-name> -n <intent-namespace> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'

kubectl get ncr
kubectl get ncr <revision> -o jsonpath='invalid={.status.isInvalid} node={.status.failedNode}{"\n"}{.status.failedMessage}{"\n"}'
kubectl get nnc <node-name> -o jsonpath='status={.status.configStatus} revision={.spec.revision} applied={.status.lastAppliedRevision}{"\n"}{.status.errorMessage}{"\n"}'
kubectl get nns <node-name> -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
kubectl get nns <node-name> -o jsonpath='{range .status.interfaces[*]}{.name} {.state} mtu={.mtu} {.addresses}{"\n"}{end}'
kubectl get nns <node-name> -o jsonpath='{range .status.routes[*]}{.destination} via {.gateway} dev {.interface} table {.table}{"\n"}{end}'
```

Interpret `Unknown` or stale observations as unknown. Object existence alone is
not proof that an interface, route, BGP session, policy, or dataplane path is
working. If `lastAppliedRevision` differs from `spec.revision`, investigate
the agent before testing traffic. A failed node can stall the revision rollout;
do not repeatedly requeue or delete the revision as a workaround.

## HBN and L2 path

For an HBN incident, first establish whether the intent actually selects HBN:
inspect the resolved destinations, VRF references, layer-2 entries, and
interface attachment in the node config. Do not infer HBN from a DebugSession
profile name. A non-HBN attachment can legitimately omit destination/VRF
plumbing.

```bash
kubectl describe nnc <node-name>
kubectl get nnc <node-name> -o yaml > <approved-evidence-dir>/nnc.yaml
kubectl get nns <node-name> -o yaml > <approved-evidence-dir>/nns.yaml

kubectl -n kube-system get pods -o wide \
  -l app.kubernetes.io/component=agent-hbn-l2
kubectl -n kube-system logs <hbn-agent-pod> -c agent-hbn-l2 --since=10m
kubectl -n kube-system logs <frr-agent-pod> -c agent-cra-frr --since=10m
kubectl -n kube-system logs <vsr-agent-pod> -c agent-cra-vsr --since=10m
```

The exact labels and container names are deployment inputs. Discover them from
the running Pod rather than assuming a platform-specific name. Verify, in
order, that the expected VLAN/sub-interface exists, its parent is up, the
resolved VRF and route are present, and the node status has a fresh observation.
If the host path is still unclear, use the approved node profile and, when
available, the deployment's bounded capture/trace helper; preserve command
metadata, not packet payloads, in the incident record.

For a single destination, `ip route get` is a safer first check than dumping
the complete routing table. Run it in the target routing context; use the VRF
form when the destination belongs to a VRF. Neighbor and bridge state can then
be captured for the exact interface:

```bash
ip route get <destination>
ip route get <destination> vrf <vrf-name>
ip neigh show dev <interface>
bridge fdb show dev <interface>
```

ARP/neighbor or bridge-FDB flushing is a controlled repair, not a diagnostic
read. Only an explicitly allowlisted `network-repair` action may flush the
smallest exact destination/interface scope, after confirmation and evidence
capture. Verify convergence with the same read commands and `NodeNetworkStatus`;
never use an unrestricted `ip neigh flush` or `bridge fdb flush` as a generic
recovery step.

## BGP, EVPN, FRR, and VSR

The classic FRR stack and the 6WIND VSR stack expose different CLIs. Select
commands from the stack actually running on the affected node and keep all
queries read-only:

```bash
# FRR: run in the approved CRA/FRR network namespace.
vtysh -c 'show bgp summary'
vtysh -c 'show bgp vrf <vrf-name> summary'
vtysh -c 'show bgp vrf <vrf-name> l2vpn evpn summary'
vtysh -c 'show bfd peers brief'

# VSR: only when the approved node profile permits the runtime namespace.
nerdctl -n <container-runtime-namespace> ps
printf 'show state fullpath / vrf <vrf-name> routing bgp\n' \
  | nerdctl -n <container-runtime-namespace> exec -i <vsr-container-id> nc-cli
```

Do not persist changes with `vtysh`, `nc-cli`, or a shell command. The
operator owns the desired configuration; repair belongs in the intent and
rollout path. Record the VRF, peer state, prefixes received/advertised, BFD
state, and the timestamp. A BGP session being established is not sufficient
proof of EVPN or application reachability: compare the expected VNI/VRF and
route with `NodeNetworkStatus`, then perform a bounded test from the approved
workload or network namespace.

For VSR, `cra-vsr` may run in a container-runtime namespace that is not the
Kubernetes namespace. A failed `nerdctl` lookup is therefore an
access/namespace diagnostic, not evidence that VSR is absent. The exact BFD
command and VRF filtering syntax also varies by FRR release; confirm it with
the installed `vtysh` help and keep the query read-only. Escalate access
failures instead of broadening host privileges.

## Netplan and host interface convergence

`NodeNetplanConfig` is desired host interface state; `NodeNetworkStatus` is the
agent-observed inventory. Compare both before and after any approved change:

```bash
kubectl get nodenetplanconfig <node-name> -o yaml > <approved-evidence-dir>/netplan-desired.yaml
kubectl get nns <node-name> -o yaml > <approved-evidence-dir>/netplan-observed.yaml
kubectl -n kube-system logs <netplan-agent-pod> -c agent-netplan --since=10m
kubectl -n kube-system logs <hbn-agent-pod> -c agent-hbn-l2 --since=10m
```

Check the node's `NodeNetworkConfig` revision and interface parent/member
relationships, then confirm the resulting link, MTU, VLAN, address, and route
in `NodeNetworkStatus`. Netplan apply is a disruptive operation on some
platform/OS combinations. Schedule changes within the deployment's change
window, use redundant workloads, and treat a transient connectivity loss as an
expected rollout risk only when the platform runbook explicitly documents it.
The operator runbook deliberately does not promise a universal downtime or OS
version behavior.

## Multus and SR-IOV boundary

The network operator's `Layer2Attachment` and `InterfaceConfig` express
network intent and node substrate requirements. They do not, by themselves,
prove that Multus, a `NetworkAttachmentDefinition` (NAD), SR-IOV device
plugin, or vendor-specific CNI is installed or configured.

```bash
kubectl get network-attachment-definitions.k8s.cni.cncf.io -n <namespace>
kubectl get network-attachment-definition.k8s.cni.cncf.io <nad> -n <namespace> -o yaml
kubectl get pod <pod> -n <namespace> -o jsonpath='{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}{"\n"}'
kubectl get node <node-name> -o jsonpath='{.status.allocatable}{"\n"}'
kubectl describe node <node-name>
```

Verify the NAD's CNI type, resource-name annotation, VLAN/trunk settings, pod
network-status, node allocatable resource, selectors, and taints as separate
facts. A workload receiving a VF is not proof that its requested VLAN or route
is correct. SR-IOV policy/NAD lifecycle must have one clearly identified
owner; the intent reconciler must not silently create a second policy or NAD.

Where an installation uses a node capability label or taint to reserve an
SR-IOV pool, discover the actual key/value and effect from the deployment
contract and verify it on the selected node. Do not assume a portable label
name, and do not treat a CNI implementation setting as an admission decision
or as proof that a VF is allocated.

Platform deployments may prohibit SR-IOV for selected sensitive traffic. Keep
that as an explicit deployment policy and admission check; do not encode
platform personas in a generic CRD or DebugSession profile.

## MultiNetworkPolicy

`MultiNetworkPolicy` is an external enforcement plane for secondary Multus
interfaces. The network operator does not claim to enforce it. Inspect the
policy and its target NAD, then test an allowed and a denied flow from a
controlled workload without changing production policy:

```bash
kubectl get multinetworkpolicies.k8s.cni.cncf.io -n <namespace>
kubectl get multinetworkpolicy.k8s.cni.cncf.io <policy> -n <namespace> -o yaml
kubectl get network-attachment-definition.k8s.cni.cncf.io <nad> -n <namespace> -o yaml
kubectl exec -n <namespace> <source-pod> -- <approved-network-test> <destination> <port>
```

Confirm the selected interface, source/destination, protocol, and policy
attachment. Test the primary Kubernetes network separately from each secondary
interface. Do not treat `Destination.spec.ports` or a successful route lookup
as MultiNetworkPolicy enforcement. If the policy daemon is absent, stale, or
cannot distinguish the target interface, report the enforcement plane as
unknown and escalate to its owner.

## Optional bounded packet-evidence integration

The following commands are an optional integration contract for a deployment
that supplies an image implementing bounded `net-debug capture` and
`net-debug trace` operations. This repository does not build or publish that
image, and a DebugSession must fail closed when the selected operation is not
available. Verify the image's own README, executable contract, digest, and
operation-specific security context before using these examples.

### Pod and host packet evidence

Use the deployment-supplied bounded helper for captures whenever possible:

```bash
net-debug capture --interface any --duration 30 --packets 1000 \
  --snaplen 128 --filter 'host 192.0.2.10 and port 443' --output capture.pcap
```

The helper limits duration, packet count, snaplen, filter length, and output
location. It prints deterministic metadata (count, size, and SHA-256), not
packet payloads. A pod-network capture requires the target pod namespace and
the approved image/session contract; a host capture requires an explicitly
approved host-network session. Do not deploy an unbounded privileged
DaemonSet, install tools into a production container, or copy a capture to an
unapproved destination.

### Kernel packet-path evidence

For kernel-level packet path evidence, use only the deployment-supplied bounded
`pwru` wrapper:

```bash
net-debug trace --duration 30 --events 1000 \
  --filter 'skb mark 0' --output trace.log
```

`pwru` requires Linux BTF, debugfs, tracefs, securityfs/LSM, and the reviewed
operation-specific capability set: `BPF`, `PERFMON`, `NET_ADMIN`, and
`SYS_RESOURCE`. The trace operation must explicitly not grant `NET_RAW`,
`SYS_ADMIN`, or `privileged: true`. If those prerequisites are absent, report
the trace as unavailable; never substitute broader privileges. Stop with the
helper's bounded SIGINT and BPF-detach windows, then verify the session-owned
trace process and evidence files are gone during cleanup.

## Evidence and escalation

Attach only the minimum evidence needed to reproduce the issue:

1. intent name/namespace and condition messages;
2. revision, node, desired/applied revision, and fresh observed status;
3. relevant agent logs with timestamps and redacted identities;
4. one bounded command result for the affected namespace/path;
5. whether Multus/SR-IOV and MultiNetworkPolicy are separately installed and
   which owner confirmed them.

State explicitly whether the finding is operator reconciliation, node
substrate, routing (FRR/VSR), external CNI/policy enforcement, or application
behavior. Unknown telemetry must remain unknown. Do not propose a CRD or
platform-specific DebugSession field merely to bridge an ownership gap.

## References

- [Operator debugging pipeline](debugging.md)
- [Node readiness](../reference/node-readiness.md)
- [CRD reference](../reference/crd-reference.md)
- [MultiNetworkPolicy project](https://github.com/k8snetworkplumbingwg/multi-networkpolicy)
- [FRRouting documentation](https://docs.frrouting.org/)
- [tcpdump filter reference](https://www.tcpdump.org/manpages/pcap-filter.7.html)
