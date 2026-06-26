# Implementation Plan — Intent-Based Network Configuration

> Derived from [Proposal 02](README.md). Tracks concrete engineering work items,
> dependencies, and deliverables. Updated as implementation progresses.

---

## Notation

- **WI** = Work Item (atomic unit of work, typically one PR)
- **Depends** = must be merged before this WI can start
- **Validates** = example or test that proves the WI works end-to-end
- ✅ Done | 🔧 In Progress | ⬜ Not Started

---

## Milestone E — E2E Test Framework (Prerequisite)

> **Goal:** A fully containerised EVPN-VXLAN test lab using kind +
> containerlab, with ginkgo-based tests validating the existing low-level
> CRDs. This framework is used by all subsequent milestones.

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-E01 | **Custom kind node image** — Dockerfile extending `kindest/node` with CRA host tooling (`umoci`, `systemd-container`, `cra-generator`, CRA scripts). Does NOT contain FRR — the cra-frr container is loaded at setup time | `e2e/images/kind-node/Dockerfile`, `e2e/images/kind-node/cra/`, `Makefile` | — | ⬜ |
| WI-E02 | **Containerlab topology** — dcgw1/dcgw2 (EVPN RR), leaf1/leaf2, kind ext-containers, m2mgw, c2mgw, tester | `e2e/clab/topology.clab.yml`, `e2e/clab/frr/` | — | ⬜ |
| WI-E03 | **FRR fabric configs** — dcgw1/dcgw2 (AS 64497, iBGP RR, VRFs m2m/c2m), leaf1/leaf2 (AS 65500, eBGP to DCGWs + servers) | `e2e/clab/frr/{dcgw1,dcgw2,leaf1,leaf2}/frr.conf` | WI-E02 | ⬜ |
| WI-E04 | **CRA base configs + loading** — per-node templates for `/etc/cra/` (base-config.yaml, netplan, frr.conf, interfaces). Script to `docker cp` + `load-cra-image.sh frr` the cra-frr OCI image into each kind node, then `systemctl start cra`. eth0 defanging via systemd-networkd | `e2e/cra-config/`, `e2e/scripts/load-cra-frr.sh`, `e2e/scripts/strip-eth0.sh` | WI-E01 | ⬜ |
| WI-E05 | **NAT64 gateway image** — tayga + unbound (dns64-synthall), startup script, DCGW route advertisement for `64:ff9b::/96` | `e2e/images/nat64/`, `e2e/clab/nat64/` | WI-E03 | ⬜ |
| WI-E06 | **Tester image** — ginkgo CLI + kubectl + Go runtime + network tools | `e2e/images/tester/Dockerfile` | — | ⬜ |
| WI-E07 | **Setup scripts** — modular 8-step sequence: build images → kind create → clab deploy → configure underlay → NAT64 → install components → kubeconfig → wait ready | `e2e/scripts/` | WI-E01…WI-E06 | ⬜ |
| WI-E08 | **Component installation** — upstream manifests for Calico, Multus, Coil, MetalLB; operator + agent via `kind load` + kustomize | `e2e/scripts/install-components.sh` | WI-E07 | ⬜ |
| WI-E09 | **Ginkgo test framework** — suite bootstrap, framework helpers (cluster, connectivity, pod, frr, wait), test config | `e2etests/e2e_suite_test.go`, `e2etests/framework/`, `e2etests/config/` | WI-E06 | ⬜ |
| WI-E10 | **Baseline test cases** — L2 connectivity (TC-01), L3 cross-VLAN (TC-02), VRF isolation (TC-03), gateway connectivity (TC-04/05), LoadBalancer (TC-06), BGP peering (TC-07), egress NAT (TC-08), anycast gateway (TC-09), NAT64 (TC-10) | `e2etests/tests/`, `e2etests/testdata/` | WI-E09 | ⬜ |
| WI-E11 | **GitHub Actions workflow** — build images → upload artifacts → matrix e2e jobs per label → collect logs on failure | `.github/workflows/e2e.yaml` | WI-E10 | ⬜ |
| WI-E12 | **Makefile integration** — `e2e-up`, `e2e-down`, `e2e-test`, `e2e-test-smoke` targets | `Makefile` | WI-E07 | ⬜ |

**Deliverable:** `make e2e-up && make e2e-test-smoke` passes on a Linux host
(or GitHub Actions runner). All 10 baseline test cases pass against the
existing low-level CRDs (`Layer2NetworkConfiguration`, `VRFRouteConfiguration`,
`BGPPeering`). The framework is ready for intent CRD tests to be added
incrementally in M1–M9.

---

## Milestone 0 — Scaffolding & Foundation Types

> **Goal:** All new Go types compile, CRDs are generated, webhooks stub out
> validation. Nothing reconciles yet — this is pure schema.

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-01 | **Define `VRFSpec` / `VRFStatus` types** | `api/v1alpha1/vrf_types.go` | — | ⬜ |
| WI-02 | **Define `NetworkSpec` / `NetworkStatus` types** | `api/v1alpha1/network_types.go` | — | ⬜ |
| WI-03 | **Define `DestinationSpec` / `DestinationStatus` types** (incl. `NextHopConfig`, `DestinationPort`, `PortRange`) | `api/v1alpha1/destination_types.go` | WI-01 (`vrfRef` type) | ⬜ |
| WI-04 | **Define shared types** — `IPNetwork`, `AdvertisementConfig` | `api/v1alpha1/shared_types.go` | — | ⬜ |
| WI-05 | **Define `Layer2AttachmentSpec` / `Layer2AttachmentStatus`** | `api/v1alpha1/layer2attachment_types.go` | WI-02, WI-04 | ⬜ |
| WI-06 | **Define `InboundSpec` / `InboundStatus`** | `api/v1alpha1/inbound_types.go` | WI-02, WI-04 | ⬜ |
| WI-07 | **Define `OutboundSpec` / `OutboundStatus`** | `api/v1alpha1/outbound_types.go` | WI-02, WI-04 | ⬜ |
| WI-08 | **Define `BGPNeighborSpec` / `BGPNeighborStatus`** (intent-level) | `api/v1alpha1/bgpneighbor_intent_types.go` | WI-06 | ⬜ |
| WI-09 | **Define `PodNetworkSpec` / `PodNetworkStatus`** | `api/v1alpha1/podnetwork_types.go` | WI-02, WI-04 | ⬜ |
| WI-10 | **Define `CollectorSpec` / `CollectorStatus`** | `api/v1alpha1/collector_types.go` | WI-01 | ⬜ |
| WI-11 | **Define `TrafficMirrorSpec` / `TrafficMirrorStatus`** | `api/v1alpha1/trafficmirror_types.go` | WI-10 | ⬜ |
| WI-12 | **Define `NodeNetworkStatusStatus`** (agent-populated) | `api/v1alpha1/nodenetworkstatus_types.go` | — | ⬜ |
| WI-13 | **Define `InterfaceConfigSpec` / `InterfaceConfigStatus`** | `api/v1alpha1/interfaceconfig_types.go` | — | ⬜ |
| WI-14 | **Run `controller-gen`** — generate CRD manifests + deep-copy | `config/crd/bases/`, `api/v1alpha1/zz_generated.deepcopy.go` | WI-01…WI-13 | ⬜ |
| WI-15 | **Stub webhook validation** for all new CRDs (accept-all initially) | `api/v1alpha1/*_webhook.go` | WI-14 | ⬜ |
| WI-16 | **Register new types in the operator manager** (`cmd/operator/main.go` scheme + RBAC markers) | `cmd/operator/main.go` | WI-14 | ⬜ |

**Deliverable:** `make generate manifests` succeeds. New CRDs can be installed
on a cluster. Example YAMLs parse correctly (resources are
persisted but nothing happens).

---

## Milestone 1 — Core Data CRDs (VRF + Network + Destination)

> **Goal:** The three foundation CRDs are fully reconciled with validation,
> status, and reference tracking. No usage-CRD controllers yet.

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-20 | **VRF controller** — watch, validate (`vrf` required, VNI/RT format), update status conditions | `controllers/operator/vrf_controller.go`, `pkg/reconciler/operator/vrf.go` (new) | WI-16 | ⬜ |
| WI-21 | **VRF webhook** — enforce `vrf` max length 12, VNI > 0, RT format | `api/v1alpha1/vrf_webhook.go` | WI-01, WI-15 | ⬜ |
| WI-22 | **Network controller** — watch, validate (≥1 of ipv4/ipv6/vlan), track usage-CRD references, update status | `controllers/operator/network_controller.go` | WI-16 | ⬜ |
| WI-23 | **Network webhook** — CIDR format, prefixLength ≥ CIDR prefix, vlan 1–4094, vni > 0 | `api/v1alpha1/network_webhook.go` | WI-02, WI-15 | ⬜ |
| WI-24 | **Destination controller** — watch, validate (vrfRef XOR nextHop), resolve VRF reference, track usage-CRD references, update status | `controllers/operator/destination_controller.go` | WI-20 | ⬜ |
| WI-25 | **Destination webhook** — vrfRef XOR nextHop mutual exclusion, prefixes CIDR format, ports validation | `api/v1alpha1/destination_webhook.go` | WI-03, WI-15 | ⬜ |
| WI-26 | **Unit tests** for VRF/Network/Destination controllers and webhooks | `controllers/operator/*_test.go`, `api/v1alpha1/*_webhook_test.go` | WI-20…WI-25 | ⬜ |

**Deliverable:** Create a `VRF`, `Network`, and `Destination` on a real cluster.
Status conditions go `Ready: True`. Invalid resources are rejected by webhooks.
Reference counts update when downstream CRDs are created (even before
downstream controllers exist — the Network controller tracks referencing
resources).

**Validates against:** any set of VRF + Network + Destination resources
should be accepted and show valid status.

---

## Milestone 2 — Layer2Attachment (HBN, non-SRIOV)

> **Goal:** The most common use case works end-to-end — a `Layer2Attachment`
> referencing a `Network` + `Destination` produces correct L2 + VRF entries
> in a `NetworkConfigRevision`.

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-30 | **Extend `ConfigReconciler` for Option B** — add intent-CRD watching alongside existing low-level CRD watches. New `fetchIntentLayer2()` / `fetchIntentVRF()` methods that read `Layer2Attachment` + resolved `Destination` + `VRF` + `Network` and produce `Layer2Revision` / `VRFRevision` entries. Merge with existing `fetchLayer2()` / `fetchLayer3()` output | `controllers/operator/config_controller.go`, `pkg/reconciler/operator/config_reconciler.go` | WI-24 | ⬜ |
| WI-31 | **Layer2Attachment controller** — watch, resolve `networkRef` → Network, resolve `destinations` selector → Destination list, validate, update status (matched destinations, resolved VRFs, conditions) | `controllers/operator/layer2attachment_controller.go` | WI-22, WI-24 | ⬜ |
| WI-32 | **Intent → L2Revision translation** — map Network (VLAN, VNI, CIDR) + Destination (vrfRef → VRF metadata) + Layer2Attachment (mtu, interfaceName, anycast, neighSuppression, nodeSelector) into `Layer2Revision` + `VRFRevision` entries | `pkg/reconciler/operator/intent_layer2.go` (new) | WI-30 | ⬜ |
| WI-33 | **Multi-destination merge** — when a Layer2Attachment selector matches multiple Destinations, merge their VRF import entries into a single VRF config (same logic as today's multi-`VRFRouteConfiguration` merge) | `pkg/reconciler/operator/intent_layer2.go` | WI-32 | ⬜ |
| WI-34 | **SBR auto-detection** — after resolving all intent attachments per node group, compare imported prefix sets. Generate intermediate local VRFs (`s-<vrf>`) + policy routes when overlap detected | `pkg/reconciler/operator/intent_sbr.go` (new) | WI-33 | ⬜ |
| WI-35 | **Layer2Attachment webhook** — networkRef required, interfaceName/interfaceRef constraints, sriov constraints | `api/v1alpha1/layer2attachment_webhook.go` | WI-05, WI-15 | ⬜ |
| WI-36 | **Conflict detection (D32)** — reject low-level `Layer2NetworkConfiguration` / `VRFRouteConfiguration` when intent CRDs exist for the same VLAN/VRF scope | `pkg/reconciler/operator/config_reconciler.go` | WI-30 | ⬜ |
| WI-37 | **Integration test** — apply VRF + Network + Destination + Layer2Attachment, verify the resulting `NetworkConfigRevision` contains correct L2 + VRF entries | `controllers/operator/layer2attachment_integration_test.go` | WI-32 | ⬜ |

**Deliverable:** Apply HBN resources (Networks, VRFs, Destinations,
Layer2Attachments). The `NetworkConfigRevision` produced matches the L2 + VRF
config currently achieved with manual `Layer2NetworkConfiguration` +
`VRFRouteConfiguration`.

---

## Milestone 3 — Inbound (HBN + non-HBN)

> **Goal:** MetalLB integration works. Inbound allocates IPs, creates
> `IPAddressPool` + `BGPAdvertisement`/`L2Advertisement`, and (in HBN mode)
> exports host routes into VRFs.

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-40 | **Inbound controller** — watch, resolve networkRef + destinations selector, allocate IPs (count or explicit addresses), update status | `controllers/operator/inbound_controller.go` | WI-22, WI-24 | ⬜ |
| WI-41 | **IP allocation logic** — allocate N IPs from a Network's CIDR, track allocations in status, detect conflicts across Inbound/Outbound sharing the same Network | `pkg/reconciler/operator/ip_allocator.go` (new) | WI-22 | ⬜ |
| WI-42 | **MetalLB resource creation** — create/update `IPAddressPool`, `BGPAdvertisement` or `L2Advertisement` based on `advertisement` field | `pkg/reconciler/operator/metallb.go` (new) | WI-40 | ⬜ |
| WI-43 | **HBN mode** — add allocated IPs as /32 host exports to matched VRFs in revision (via ConfigReconciler Option B path) | `pkg/reconciler/operator/intent_inbound.go` (new) | WI-30, WI-40 | ⬜ |
| WI-44 | **Non-HBN mode** — when destinations is omitted, produce only MetalLB resources (no VRF plumbing) | `controllers/operator/inbound_controller.go` | WI-42 | ⬜ |
| WI-45 | **Inbound webhook** — networkRef required, count > 0, count XOR addresses, Network must have IPs | `api/v1alpha1/inbound_webhook.go` | WI-06, WI-15 | ⬜ |
| WI-46 | **Integration test** — apply Inbound (HBN), verify IPAddressPool + BGPAdvertisement created, verify VRF host exports in revision | `controllers/operator/inbound_integration_test.go` | WI-43 | ⬜ |
| WI-47 | **Integration test** — apply Inbound (non-HBN, L2 advertisement), verify IPAddressPool + L2Advertisement, no VRF entries | `controllers/operator/inbound_integration_test.go` | WI-44 | ⬜ |

**Deliverable:** HBN Inbound resources produce correct MetalLB pools and VRF
host exports. Non-HBN Inbound produces MetalLB resources without VRF plumbing.

---

## Milestone 4 — Outbound (HBN + non-HBN)

> **Goal:** Egress NAT works. Outbound allocates IPs, creates Coil `Egress` +
> Calico `IPPool`/`NetworkPolicy`, and (in HBN mode) exports host routes +
> derives egressNAT CIDRs from Destination prefixes (D42).

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-50 | **Outbound controller** — watch, resolve networkRef + destinations, allocate IPs, update status | `controllers/operator/outbound_controller.go` | WI-41 | ⬜ |
| WI-51 | **Coil Egress resource creation** — create/update Coil `Egress` with replicas + IPs | `pkg/reconciler/operator/coil.go` (new) | WI-50 | ⬜ |
| WI-52 | **Calico resource creation** — create/update Calico `IPPool` + `NetworkPolicy` (with per-Destination port rules from D43) | `pkg/reconciler/operator/calico.go` (new) | WI-50 | ⬜ |
| WI-53 | **EgressNAT derivation (D42)** — derive egressNAT CIDRs from union of matched Destination prefixes (no separate field) | `pkg/reconciler/operator/intent_outbound.go` (new) | WI-50 | ⬜ |
| WI-54 | **HBN mode** — add allocated IPs as /32 host exports to matched VRFs in revision | `pkg/reconciler/operator/intent_outbound.go` | WI-30, WI-50 | ⬜ |
| WI-55 | **Outbound webhook** — networkRef required, count > replicas, count XOR addresses | `api/v1alpha1/outbound_webhook.go` | WI-07, WI-15 | ⬜ |
| WI-56 | **Integration test** — apply Outbound (HBN), verify Coil Egress + Calico resources + VRF exports + egressNAT | `controllers/operator/outbound_integration_test.go` | WI-53 | ⬜ |

**Deliverable:** HBN Outbound resources produce correct Coil + Calico resources
and VRF exports.

---

## Milestone 5 — Layer2Attachment Extensions

> **Goal:** Non-HBN, SR-IOV, and pure-L2 modes for Layer2Attachment.

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-60 | **Non-HBN `interfaceRef` mode** — create VLAN sub-interface on named physical interface/bond instead of VXLAN tunnel. Default interfaceName to `vlan.<vlanID>` when omitted | `pkg/reconciler/operator/intent_layer2.go` | WI-32 | ⬜ |
| WI-61 | **Non-HBN static routing** — when Destination has `nextHop`, generate static routes for each prefix via the next-hop address on the sub-interface | `pkg/reconciler/operator/intent_layer2.go` | WI-60 | ⬜ |
| WI-62 | **SR-IOV support** — when `sriov.enabled`, create SR-IOV policies for `NetworkAttachmentDefinition` using VLAN from Network | `pkg/reconciler/operator/sriov.go` (new) | WI-31 | ⬜ |
| WI-63 | **SR-IOV + interfaceName** — create host interface attached to SR-IOV bridge when both sriov and interfaceName are set | `pkg/reconciler/operator/sriov.go` | WI-62 | ⬜ |
| WI-64 | **Pure L2 mode** — Network with VLAN only (no IPs, no VNI), Layer2Attachment with interfaceRef, no destinations → produce only VLAN sub-interface config, no VRF, no anycast | `pkg/reconciler/operator/intent_layer2.go` | WI-60 | ⬜ |
| WI-65 | **Integration test** — apply pure-L2 resources, verify VLAN sub-interfaces on bond, no VRF entries | `controllers/operator/layer2attachment_integration_test.go` | WI-64 | ⬜ |

**Deliverable:** All three deployment modes work:
- HBN (VXLAN + VRF)
- Non-HBN (L2 advertisement, interfaceRef)
- Pure L2 on bond (VLAN only, no IP, no VRF)

---

## Milestone 6 — NodeNetworkStatus + InterfaceConfig

> **Goal:** Operational visibility into node interfaces. Bond/VF provisioning
> via intent CRD.

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-70 | **NodeNetworkStatus CRD** — agent-populated, status-only. Define types (already done in WI-12) | (types from WI-12) | WI-12 | ⬜ |
| WI-71 | **CRA agent extension** — report node interfaces (excl. pod veths), routes, IPs to `NodeNetworkStatus` resource. Periodic updates | `pkg/cra-frr/` or `pkg/cra-vsr/` agent code, `controllers/agent-cra-frr/` | WI-70 | ⬜ |
| WI-72 | **`interfaceRef` validation** — Layer2Attachment controller reads `NodeNetworkStatus` for nodes in `nodeSelector` scope and validates `interfaceRef` exists. Sets `InterfaceNotFound` condition if missing | `controllers/operator/layer2attachment_controller.go` | WI-71, WI-31 | ⬜ |
| WI-73 | **InterfaceConfig controller** — watch, resolve `nodeSelector` → node list, generate/update `NodeNetplanConfig` per matched node. Merge multiple InterfaceConfigs per node (reject conflicts) | `controllers/operator/interfaceconfig_controller.go` (new) | WI-16 | ⬜ |
| WI-74 | **InterfaceConfig webhook** — nodeSelector required, bond members must be valid, virtualFunctionCount > 0, valid bond mode | `api/v1alpha1/interfaceconfig_webhook.go` | WI-13, WI-15 | ⬜ |
| WI-75 | **InterfaceConfig status** — per-node apply status from `NodeNetplanConfig` conditions | `controllers/operator/interfaceconfig_controller.go` | WI-73 | ⬜ |
| WI-76 | **Integration test** — apply InterfaceConfig for bond2, verify NodeNetplanConfig created per matched node with correct ethernets + bond spec | `controllers/operator/interfaceconfig_integration_test.go` | WI-73 | ⬜ |

**Deliverable:** An InterfaceConfig for bond2 creates `NodeNetplanConfig`
resources for matched nodes. `NodeNetworkStatus` shows bond2 after agent-netplan
applies it. Layer2Attachment validates `interfaceRef: bond2` against reported
interfaces.

---

## Milestone 7 — BGPNeighbor + PodNetwork

> **Goal:** Advanced networking — workload BGP and additional pod networks.

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-80 | **BGPNeighbor controller** — watch, resolve referenced Inbound resources, translate into revision pipeline (BGP session config) | `controllers/operator/bgpneighbor_intent_controller.go` | WI-30, WI-40 | ⬜ |
| WI-81 | **BGPNeighbor webhook** — referenced Inbounds must exist, must not have MetalLB controller | `api/v1alpha1/bgpneighbor_intent_webhook.go` | WI-08, WI-15 | ⬜ |
| WI-82 | **PodNetwork controller** — watch, resolve destinations, configure Calico IPPool, set up VRF routes via ConfigReconciler | `controllers/operator/podnetwork_controller.go` | WI-30, WI-24 | ⬜ |
| WI-83 | **PodNetwork webhook** — networkRef required, Network must have IPs | `api/v1alpha1/podnetwork_webhook.go` | WI-09, WI-15 | ⬜ |
| WI-84 | **Integration tests** | `controllers/operator/*_integration_test.go` | WI-80, WI-82 | ⬜ |

**Deliverable:** BGPNeighbor produces correct BGP session config in revision.
PodNetwork produces Calico IPPool + VRF routes.

---

## Milestone 8 — Traffic Mirroring (Collector + TrafficMirror)

> **Goal:** Intent-level traffic mirroring on top of Proposal 01 low-level
> implementation.

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-90 | **Collector controller** — watch, resolve mirrorVRF → VRF (with loopbacks), CAPI IPAM integration (IPAddressClaim per node in scope), feed mirror VRF + GRE tunnel into ConfigReconciler | `controllers/operator/collector_controller.go` | WI-30, WI-20 | ⬜ |
| WI-91 | **TrafficMirror controller** — watch, resolve source attachment + collector, generate MirrorACL entries in revision | `controllers/operator/trafficmirror_controller.go` | WI-90, WI-31 | ⬜ |
| WI-92 | **Collector webhook** — address is valid IP, protocol enum, mirrorVRF.name must exist and have loopbacks | `api/v1alpha1/collector_webhook.go` | WI-10, WI-15 | ⬜ |
| WI-93 | **TrafficMirror webhook** — source kind enum, source name exists, collector exists | `api/v1alpha1/trafficmirror_webhook.go` | WI-11, WI-15 | ⬜ |
| WI-94 | **Integration test** — apply Collector + TrafficMirror, verify MirrorACL + GRE tunnel in revision | `controllers/operator/mirror_integration_test.go` | WI-91 | ⬜ |

**Deliverable:** Collector + TrafficMirror produce correct mirror VRF +
GRE tunnel + MirrorACL entries in `NetworkConfigRevision`.

**Dependency:** Requires Proposal 01 (traffic mirroring low-level CRDs) to be
implemented first for the underlying `MirrorTarget` / `MirrorSelector` pipeline.

---

## Milestone 9 — Migration Tooling

> **Goal:** Existing clusters can adopt intent CRDs without downtime.

| # | Work Item | Files | Depends | Status |
|---|---|---|---|---|
| WI-100 | **Migration CLI** — read existing `Layer2NetworkConfiguration` + `VRFRouteConfiguration` + `BGPPeering` and generate equivalent intent CRDs (VRF + Network + Destination + Layer2Attachment + Inbound/Outbound) | `cmd/migrate/` (new) or `hack/migrate.go` | M2, M3, M4 | ⬜ |
| WI-101 | **SchiffCluster config import** — read `schiff-network` ConfigMap format and generate intent CRDs | `cmd/migrate/` | WI-100 | ⬜ |
| WI-102 | **Diff tool** — compare generated `NetworkConfigRevision` from intent CRDs vs existing low-level CRDs, report differences | `cmd/migrate/` | WI-100 | ⬜ |
| WI-103 | **Migration runbook** — step-by-step guide: install CRDs → run migration tool → apply intent CRDs → verify revision matches → remove low-level CRDs | `docs/migration.md` | WI-100 | ⬜ |

**Deliverable:** A cluster running on low-level CRDs can switch to intent CRDs
with the migration tool, verify equivalence, and cut over without node
disruption.

---

## Dependency Graph (Milestones)

```
ME ──▶ M0 ──▶ M1 ──┬──▶ M2 ──┬──▶ M3 ──▶ M4
                    │         │
                    │         └──▶ M5
                    │
                    ├──▶ M6
                    │
                    └──▶ M7 ──▶ M8

M2 + M3 + M4 ──▶ M9
```

- **ME** (E2E framework) gates everything — baseline tests validate the
  existing low-level CRDs and provide the harness for all subsequent milestones.
- **M0** (types) gates all controller work.
- **M1** (VRF/Network/Destination) gates all usage CRDs.
- **M2** (Layer2Attachment HBN) is the first real value delivery — it introduces
  the Option B `ConfigReconciler` extension that M3, M4, M5, M7, M8 all build on.
- **M3** (Inbound) and **M4** (Outbound) can proceed in parallel once M2 lands.
- **M5** (Layer2Attachment extensions) can proceed once M2 lands.
- **M6** (NodeNetworkStatus + InterfaceConfig) depends only on M1 for types and
  is otherwise independent — good for parallel work.
- **M7** (BGPNeighbor + PodNetwork) depends on M1 + M2.
- **M8** (Mirroring) depends on M7 and Proposal 01.
- **M9** (Migration) depends on M2 + M3 + M4 being stable.

---

## Suggested Sprint Allocation

Assuming 2-week sprints with 2–3 engineers:

| Sprint | Focus | Milestones |
|---|---|---|
| S0 | E2E framework: kind image, clab topology, fabric configs, NAT64, setup scripts | ME (WI-E01…E08) |
| S0 | E2E tests: ginkgo framework, baseline test cases, CI workflow | ME (WI-E09…E12) |
| S1 | Types, CRD generation, webhook stubs | M0 |
| S2 | VRF + Network + Destination controllers + webhooks | M1 |
| S3–S4 | Layer2Attachment HBN + ConfigReconciler Option B | M2 |
| S5 | Inbound (HBN + non-HBN) + MetalLB integration | M3 |
| S5 | NodeNetworkStatus agent + InterfaceConfig (parallel track) | M6 (start) |
| S6 | Outbound (HBN + non-HBN) + Coil/Calico integration | M4 |
| S6 | InterfaceConfig controller + interfaceRef validation (parallel) | M6 (finish) |
| S7 | Layer2Attachment extensions (non-HBN, SR-IOV, pure L2) | M5 |
| S8 | BGPNeighbor + PodNetwork | M7 |
| S9 | Collector + TrafficMirror (if Proposal 01 is ready) | M8 |
| S10 | Migration tooling + runbook | M9 |

---

## Key Technical Risks

| Risk | Impact | Mitigation |
|---|---|---|
| **ConfigReconciler Option B complexity** — extending `fetchConfigData()` to merge intent + low-level CRDs into a single revision | High — this is the architectural pivot point | Start with read-only: `fetchIntentLayer2()` produces `Layer2Revision` entries identical in shape to `fetchLayer2()`. Merge is a simple `append`. Conflict detection (D32) can be a second pass |
| **SBR auto-detection correctness** — comparing prefix sets across all attachments per node group, generating intermediate VRFs | High — incorrect SBR causes routing failures | Port existing `sbrPrefixes` logic into the intent path. Extensive test matrix: no overlap, partial overlap, full overlap, multi-VRF |
| **IP allocation conflicts** — multiple Inbound/Outbound sharing a Network's CIDR | Medium — duplicate IPs break MetalLB/Coil | Centralised allocator with optimistic locking on Network.status. Detect conflicts at webhook time where possible |
| **MetalLB / Coil version coupling** — API changes in newer releases | Medium — integration may break on upgrade | Isolate MetalLB/Coil client code behind interfaces. Pin tested versions. D30 |
| **Multi-InterfaceConfig merge** — conflicting specs for the same node | Low — only affects InterfaceConfig | Reject conflicts at controller level (not webhook — needs runtime node resolution). Clear error conditions |

---

## Acceptance Criteria (per Milestone)

| Milestone | Criteria |
|---|---|
| ME | `make e2e-up` succeeds in < 15 min. All 10 baseline test cases pass against low-level CRDs. `make e2e-test-smoke` passes in GitHub Actions. NAT64 outbound works. |
| M0 | `make generate manifests` passes. CRDs installable. Example YAMLs parse |
| M1 | VRF/Network/Destination accepted, validated, status Ready. Reference counts update |
| M2 | Layer2Attachment + Network + Destination + VRF → `NetworkConfigRevision` matches expected L2+VRF config. SBR auto-detected. Conflict detection rejects overlapping low-level CRDs |
| M3 | Inbound (HBN) → MetalLB pool + VRF host exports. Inbound (non-HBN) → MetalLB pool only |
| M4 | Outbound → Coil Egress + Calico IPPool/NetworkPolicy + VRF exports. EgressNAT CIDRs derived from Destinations |
| M5 | Pure-L2, interfaceRef, SR-IOV modes all produce correct revision entries. All three example sets work |
| M6 | NodeNetworkStatus populated by agent. InterfaceConfig → NodeNetplanConfig per node. interfaceRef validation works |
| M7 | BGPNeighbor → BGP session in revision. PodNetwork → Calico IPPool + VRF routes |
| M8 | Collector + TrafficMirror → mirror VRF + GRE + MirrorACL in revision |
| M9 | Migration tool converts low-level → intent CRDs. Diff tool confirms revision equivalence |
