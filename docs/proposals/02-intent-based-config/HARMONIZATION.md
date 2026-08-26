# Generic debug-intent harmonization

This note is the compatibility contract between the network operator's
networking intents and the shared breakglass utility catalogue. It uses the
catalogue's stable, generic intent names and does not add platform-specific
identity or deployment names to the network-operator API.

The seven catalogue intent names are:

1. `workload-diagnostics`
2. `network-diagnostics`
3. `storage-diagnostics`
4. `dump-access`
5. `network-repair`
6. `node-recovery`
7. `cluster-validation`

The names and their profile contract are defined by the [breakglass catalogue
interface](https://github.com/telekom/k8s-breakglass/blob/f9dbcb98d87ae284682f87ac821ee9d00ab8fe99/docs/debug-session-catalogue-interface.md).
A catalogue profile is an approved debugging workflow; it is not a
network-operator CRD.

## Capability mapping

| Existing capability or artifact | Generic intent | Network-operator side | Boundary / status |
|---|---|---|---|
| Workload shell, pod/service/DNS checks | `workload-diagnostics` | None | The workload-debug image and DebugSession catalogue own this workflow. Do not make a network CRD a proxy for a shell. |
| Legacy `netshoot` / network-debug pod | `network-diagnostics` | HBN and non-HBN `Network`, `Destination`, `Layer2Attachment`, `Inbound`, and `Outbound` describe the path being inspected. `NodeNetworkConfig` is the resolved per-node view. | The profile may observe this path, but does not grant Kubernetes API access or network mutation. |
| Legacy node-shell / host network debug | `node-recovery` | `NodeNetworkStatus` is the read-only inventory; CRA agents consume `NodeNetworkConfig` and report apply status. | Host namespaces and recovery actions are a breakglass/controller policy decision, not an operator default. |
| Allowlisted link/interface repair | `network-repair` | `InterfaceConfig` expresses desired bonds, MTU, and VF count; it resolves to `NodeNetplanConfig` for `agent-netplan`. CRA applies resolved `NodeNetworkConfig`. | The operator has no generic repair command and must not treat `InterfaceConfig` as an interactive shell. A separate, allowlisted utility is required. |
| Storage/dump investigation | `storage-diagnostics` / `dump-access` | None | These intents map to the shared utility images. They are deliberately not represented by networking CRDs. |
| Operator/cluster/network health inventory | `cluster-validation` | Intent status conditions, `NodeNetworkStatus`, legacy revision/NNC status, CRA health and metrics are evidence sources. | There is no single cluster-validator implementation in this repository. A validator must consume these sources read-only and must not infer readiness from object existence alone. |
| Traffic mirroring (`MirrorSelector` / `MirrorTarget`) | `network-diagnostics` | `Collector` + `TrafficMirror` resolve to GRE `MirrorACL` entries on `NodeNetworkConfig`; mirror VRF metadata is a `VRF` with loopbacks. | The generic profile is only the access workflow. Collector lifecycle, capture retention, and data handling remain platform policy. |
| SR-IOV VF observation | `network-diagnostics` | `NodeNetworkStatus` can expose the host interface inventory; `Layer2Attachment.sriov` identifies SR-IOV attachment intent. | No implicit privilege escalation or host networking follows from this mapping. |
| SR-IOV VF provisioning / VLAN attachment | `network-repair` (when an approved change is intended) | `InterfaceConfig` provisions `virtualFunctionCount`; `Layer2Attachment` carries `sriov.enabled` and the referenced `Network.vlan`. | The current intent reconciler does not create an SR-IOV policy or `NetworkAttachmentDefinition`; SR-IOV policy/operator integration remains a required downstream implementation. |
| MultiNetworkPolicy enforcement | `network-diagnostics` (observe only) | None in this repository | Enforcement is provided by the separate `multi-networkpolicy-nftables` daemon. `Destination.spec.ports` is a routing/egress-policy input for the intent stack and is not a MultiNetworkPolicy replacement. |

## Scope model

The two systems use different scope dimensions and must not be conflated:

| Dimension | Network operator | Breakglass catalogue integration |
|---|---|---|
| Physical target | `spec.nodeSelector` on usage resources; `NodeNetworkStatus` and NNC are per node | `targetNode` is a constrained per-session variable for node maintenance profiles |
| Network intent | `Network`, `VRF`, `Destination`, and usage CRDs | The debug profile describes how to inspect or change a path; it does not create the path |
| Platform vs tenant | Tenant-cluster operator consumes resolved values. Management-cluster provisioning and harmonisation are explicitly upstream concerns (`NetworkBinding` future design) | Requester/approver groups and cluster selectors are copied into DebugSessionTemplates; installation must not infer access from namespace or environment |
| Service integration (SI) | No SI-specific API or identity assumptions | Bindings and external policy decide which service/platform/tenant identities can request or approve a generic profile |
| HBN / non-HBN | HBN is selected by resolved destinations; omitting destinations is non-HBN. `interfaceRef` covers physical/bond/SR-IOV attachment | A profile may be configured for a target cluster/node, but must not infer HBN or non-HBN from a profile name |

Use labels and bindings at the integration boundary when a deployment needs
tenant, platform, or SI personas. Do not encode those persona names in
`network-connector.sylvaproject.org` resources. The operator should receive
resolved, cluster-local network intents and standard Kubernetes selectors.

## Current implementation gaps

These are deliberate compatibility gaps, not reasons to add platform-specific
fields to the operator:

1. **Legacy coexistence is not implemented.** Enabling
   `--enable-intent-reconciler` disables the legacy `ConfigReconciler` (see
   `cmd/operator/main.go`). `pkg/reconciler/intent/legacy/detector.go` only
   logs that legacy resources exist; it does not merge them, reject conflicting
   scopes, or preserve their output. This contradicts the proposal's D24/D32
   wording and is a migration blocker. Until a merge/rejection implementation
   lands, run one reconciliation mode per cluster and remove legacy resources
   only after comparing resolved output.
2. **SR-IOV orchestration is incomplete.** `Layer2Attachment.sriov` and
   `InterfaceConfig` describe the intent and VF count, but the intent builders
   contain no SR-IOV policy/NAD writer. A downstream SR-IOV operator or a
   clearly-owned controller must create and validate those resources.
3. **MultiNetworkPolicy is an external enforcement plane.** The network
   operator's intent controller must not claim to enforce the
   `MultiNetworkPolicy` CRD. Deploy, authorize, and test
   `multi-networkpolicy-nftables` separately when that capability is required.
4. **Cluster validation is an integration consumer.** `NodeNetworkStatus` is
   an inventory contract, not a complete validator. A validator should check
   conditions and actual routes/interfaces/addresses and should report missing
   telemetry as unknown rather than healthy.
5. **The management-cluster harmonisation pipeline is future work.** BM4X
   harmonisation, allocation, and `NetworkBinding` ordering belong upstream.
   The tenant operator accepts resolved VLAN/VNI/CIDR/RT values and must not
   call the provisioning backend or infer a deployment-specific network name.

## Verification contract

An integration review should prove each mapping without relying on names alone:

- `network-diagnostics`: inspect a resolved NNC and CRA/NodeNetworkStatus
  evidence for the selected node and network path; separately verify
  MultiNetworkPolicy enforcement if installed.
- `network-repair`: require an exact node, interface, allowlisted action, and
  confirmation token; verify evidence output and that no unrestricted exec is
  enabled.
- `node-recovery`: require explicit elevated approval and bounded host access;
  verify cleanup after expiry.
- `cluster-validation`: require read-only credentials only when API checks are
  needed, and distinguish `Ready`, `NotReady`, and `Unknown` evidence.
- `storage-diagnostics`, `dump-access`, and `workload-diagnostics`: verify
  their utility/image contracts independently of network-operator rollout.

This mapping is intentionally one-way: generic debug intents may inspect a
network-operator deployment, while network-operator CRDs must remain generic
and usable without breakglass or any particular identity system.
