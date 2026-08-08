# vhost-user: the 6WIND Device Plugin Contract, and What Our CNI Must Do

- **Status:** Findings / reference
- **Date:** 2026-08-08
- **Subject:** `nc-k8s-plugin-hna` v3.12.2 (`hna-device-plugin`)
- **Audience:** whoever implements `cni-workload`'s vhost-user transport

## 1. Why this document exists

`pkg/cni/vhostuser.go` and the reference manifests under `e2e/kubevirt/manifests/`
were written **blind** — they carry comments to that effect:

> *"It cannot be exercised without the 6WIND device plugin and a real VSR, so it
> is implemented against the reference NAD/VM manifests and validated blind."*

The goal in the network-operator is to **implement the CNI plugin**. This
document therefore describes the contract from the CNI's point of view: what
arrives at `cmdAdd`, where it came from, and what the plugin is obliged to do
with it.

Read in order:

- **§3** — how a `resourceName` on the NAD turns into a `deviceID` in our config.
  This is the plumbing chain, and it is the part that was guessed at.
- **§4–6** — what the 6WIND plugin puts on the node.
- **§7** — **what our CNI must do**, concretely.
- **§8** — where the current implementation is wrong.

## 2. Provenance and confidence

Two different sources are mixed here, and they are not equally reliable:

- **The Multus plumbing (§3)** was read directly from the **Multus source** we
  vendor. It is verifiable in-tree and can be trusted.
- **The 6WIND plugin behaviour (§4–6)** was determined by **inspecting the
  shipped plugin artefacts** (the `hna-device-plugin` image and its bundled
  application) — not from vendor documentation, not from source access, and
  **not from a live cluster run**.

Findings are tagged:

- **[established]** — unambiguous from the artefact or from Multus source.
- **[inferred]** — a reasonable reading, but a detail could differ at runtime.
- **[unverified]** — must be confirmed on real hardware before being relied on.

Section 10 collects everything to re-check on a real HNA node. Nothing in §4–6
should be treated as vendor-documented behaviour.

## 3. How `resourceName` becomes `deviceID`

This is the chain the NAD author never sees. All of it is **[established]** from
the Multus source in `pkg/k8sclient/k8sclient.go` and `pkg/types/conf.go`.

**Step 1 — the NAD carries the resource name as an annotation.** Not a field in
the CNI config; an annotation on the `NetworkAttachmentDefinition` object:

```yaml
metadata:
  annotations:
    k8s.v1.cni.cncf.io/resourceName: nc-k8s-plugin.6wind.com/virtio-user
```

**Step 2 — the pod separately requests that resource.** The annotation alone
allocates nothing. The pod spec must also carry a matching
`resources.limits` entry, which is what actually makes kubelet call the device
plugin's `Allocate()`:

```yaml
resources:
  limits:
    nc-k8s-plugin.6wind.com/virtio-user: "1"
```

These two must agree. A NAD annotation with no matching resource request yields
an empty `deviceID` and no error — a silent misconfiguration.

**Step 3 — Multus builds a resource map for the pod.** When it sees the
annotation, it asks the kubelet pod-resources API (and the DRA client) which
device IDs were allocated to this pod, keyed by resource name.

**Step 4 — Multus picks *one* device ID per attachment, by cursor.** This is the
detail that matters for multi-interface pods:

```go
entry, ok := resourceMap[resourceName]
if ok {
    if idCount := len(entry.DeviceIDs); idCount > 0 && idCount > entry.Index {
        deviceID = entry.DeviceIDs[entry.Index]
        entry.Index++ // increment Index for next delegate
    }
}
```

So Multus keeps a per-resource cursor and hands out allocated device IDs
**in order, one per network attachment**, as it walks the pod's network
annotation list. Two attachments against the same resource get
`DeviceIDs[0]` and `DeviceIDs[1]` respectively. Attachments beyond the number of
allocated devices silently get an **empty** `deviceID`.

**Step 5 — Multus injects it into the delegate config.** It rewrites our CNI
config JSON before invoking us, adding **two** top-level keys:

```go
rawConfig["deviceID"] = deviceID
rawConfig["pciBusID"] = deviceID
```

Note `pciBusID` is set to the same value even though this is not a PCI device —
it is unconditional in Multus. Ignore it; read `deviceID`.

So at `cmdAdd` our `NetConf.DeviceID` is populated **as a plain top-level field
of the CNI config**. The `capabilities: { "deviceID": true }` route
(`RuntimeConfig.DeviceID`) is a separate mechanism used by some runtimes; our
config struct already handles both, and should prefer whichever is non-empty.

**Step 6 — Multus pre-copies the device plugin's device-info file for us.**
This one is easy to miss and removes work from our plugin:

```go
if cniDeviceInfoPath != "" && delegate.ResourceName != "" && delegate.DeviceID != "" {
    err = nadutils.CopyDeviceInfoForCNIFromDP(cniDeviceInfoPath, delegate.ResourceName, delegate.DeviceID)
}
```

When the NAD requests the `CNIDeviceInfoFile` capability, Multus reads the
**device plugin's** file and writes it to **our** CNI device-info path *before*
calling us. The paths are fixed by convention:

| File | Path |
|---|---|
| written by the device plugin | `/var/run/k8s.cni.cncf.io/devinfo/dp/<resourceName with "/"→"-">-<deviceID>-device.json` |
| handed to / owned by the CNI | `/var/run/k8s.cni.cncf.io/devinfo/cni/<filename chosen by Multus>` |

The `-device.json` suffix and the `/`→`-` substitution are fixed in the NPWG
client library, so for our case the plugin's file is at:

```
/var/run/k8s.cni.cncf.io/devinfo/dp/nc-k8s-plugin.6wind.com-virtio-user-<deviceID>-device.json
```

The copy is **best-effort** — Multus logs and ignores a failure. So the file may
or may not be there, and our plugin must handle both.

### 3.1 What this means for us

> Our CNI receives a `deviceID`. **That single opaque string is the whole
> input**, and everything else — the host socket path, the pod socket path, the
> mode — is either derived from it or read from the device-info file that
> Multus has already staged for us.

## 4. What the plugin advertises

The plugin registers under the resource domain `nc-k8s-plugin.6wind.com` and
advertises **four** resources **[established]**:

| Resource | Meaning |
|---|---|
| `nc-k8s-plugin.6wind.com/vhost-user` | one socket, workload is the **vhost-user** end |
| `nc-k8s-plugin.6wind.com/virtio-user` | one socket, workload is the **virtio-user** end |
| `nc-k8s-plugin.6wind.com/vhost-user-all` | the **whole** vhost-user socket directory |
| `nc-k8s-plugin.6wind.com/virtio-user-all` | the **whole** virtio-user socket directory |

It talks to kubelet over the standard device-plugin gRPC socket directory
`/var/lib/kubelet/device-plugins` **[established]**.

There are two socket trees on the host **[established]**:

| Host directory | Who owns the listening socket |
|---|---|
| `/run/vsr-vhost-user` | **vSR** is the backend/server; workloads connect to it |
| `/run/vsr-virtio-user` | a **workload** is the backend; vSR connects to it |

## 5. What an allocation actually yields

For the two per-device resources, an `Allocate()` produces a bind mount whose
**host and container prefixes are deliberately crossed** **[established]**:

| Requested resource | Host path | Path inside the container |
|---|---|---|
| `vhost-user` | `/run/vsr-virtio-user/{device-id}` | `/run/vsr-vhost-user/{index}` |
| `virtio-user` | `/run/vsr-vhost-user/{device-id}` | `/run/vsr-virtio-user/{index}` |

This is not a typo in the plugin. The naming describes **the role of the far
end**: a workload that requests `virtio-user` is itself the virtio-user client,
so the socket it connects to lives in the tree where *vSR* is the vhost-user
backend. Each side is named after what it is talking *to*.

The consequence that matters most for us:

> **The host path and the container path of the same socket are different, and
> neither is chosen by the NAD author.** The host path is keyed by an opaque
> device ID; the container path is keyed by a small ordinal.

Alongside the mount, an allocation also produces:

- **An environment variable** `HNA_<RESOURCE>_DEVICES`, with the resource name
  upper-cased and hyphens replaced, holding a comma-separated list of the
  allocated device IDs — e.g. `HNA_VIRTIO_USER_DEVICES=3f9a2b1c7d`
  **[established]**. This is injected by **kubelet**, not by Multus.
- **A device-info JSON file** written by the plugin under
  `/run/k8s.cni.cncf.io/devinfo/dp/` **[established]**, following the
  k8snetworkplumbingwg device-info convention, with content of the shape:

  ```json
  {
    "type": "vhost-user",
    "version": "1.1.0",
    "vhost-user": { "mode": "server", "path": "/run/vsr-virtio-user/0/socket" }
  }
  ```

  The `path` is the **container-side** path plus a literal `socket` filename
  **[established]**. The filename of the JSON itself is fixed by the NPWG client
  library rather than by 6WIND — `<resourceName>-<deviceID>-device.json` with
  `/` replaced by `-` — so for our resource it is
  `nc-k8s-plugin.6wind.com-virtio-user-<deviceID>-device.json` **[established]**.
  The `mode` value is **[unverified]** — see §8.3.

### 5.1 Identifiers

| Identifier | Shape | Lifetime |
|---|---|---|
| device ID | random 10-character hex string | generated **once when the plugin starts** — **not stable across plugin restarts** **[established]** |
| index | small integer, `0`, `1`, `2`, … | counted **fresh for every `Allocate()` call** **[established]** |

Two things follow, and both are load-bearing:

1. **Device IDs must never be persisted** in CRs, or cached across a plugin
   restart. They are ephemeral handles, not stable hardware identities.
2. **The container-side index always starts at `0` per pod.** A pod that
   requests one socket always sees it at `.../0/socket`, regardless of how many
   other pods exist on the node.

### 5.2 More than one socket in a pod

If a pod requests `nc-k8s-plugin.6wind.com/virtio-user: "2"`, kubelet performs a
single `Allocate()` for both, and the pod sees **`/run/vsr-virtio-user/0`** and
**`/run/vsr-virtio-user/1`** **[established]**. The pairing between those
indices and the pod's *network attachments* is **not** expressed by the device
plugin at all — it falls out of the order in which Multus consumes the allocated
device IDs when it walks the pod's network annotations **[inferred]**.

This is the multi-attachment ordering hazard: **index `0` is not guaranteed to
belong to the first NAD in the annotation** unless Multus's allocation cursor
and the plugin's counter happen to agree. Any implementation that assumes
positional correspondence should be treated as unproven until tested with two
attachments — see §8.

The `-all` variants side-step this entirely by mounting the whole directory, at
the cost of exposing every socket on the node to the pod.

## 6. What the plugin does *not* do

This is the shortest and most important section **[established]**:

- It does **not** create the fast-path port on the vSR side. Nothing in the
  plugin speaks to vSR, NETCONF, or the fast path.
- It does **not** create, bind or listen on the socket itself.
- It does **not** do any CNI work: no netdev, no IP address, no route.
- It does **not** allocate or track VLANs, VNIs, or any network identity.

Its entire job is: **hand a pod a directory, an environment variable, and a
device-info file.** Everything that makes the socket carry traffic is somebody
else's responsibility — ours.

## 7. What our CNI must do

This is the actionable section. Assume a NAD annotated with
`nc-k8s-plugin.6wind.com/virtio-user` and a pod that requests one of them.

### 7.1 On `cmdAdd`

1. **Read `deviceID`.** Prefer `RuntimeConfig.DeviceID` (capability route) and
   fall back to the top-level `DeviceID` that Multus injects (§3, step 5). If
   both are empty, either the pod did not request the resource or the cursor ran
   out of devices (§3, step 4) — **fail with a clear message** rather than
   falling back to a static path, because a silent fallback produces a socket
   that never connects.

2. **Try the staged device-info file first.** Multus has likely already copied
   the plugin's file to our CNI device-info path (§3, step 6). If it parses,
   take the socket path and mode from it — it is the plugin's own statement of
   what it allocated, and it is authoritative.

3. **Otherwise derive the paths from the `deviceID`.** The two paths are *not*
   the same, and picking the wrong one is the single easiest way to break this:

   | Consumer | Namespace | Path |
   |---|---|---|
   | our CNI, and the CRA-VSR agent | host mount ns | `/run/vsr-vhost-user/<deviceID>/socket` |
   | the KubeVirt hook and the VM domain | pod mount ns | `/run/vsr-virtio-user/<index>/socket` |

   (for a pod requesting `virtio-user`; the two trees swap for `vhost-user`)

   We cannot reliably compute `<index>` ourselves — it is assigned by the plugin
   per `Allocate()` (§5.1). This is the strongest argument for treating the
   staged device-info file as the primary source and derivation as the fallback.

4. **Publish the device-info file for downstream.** Write the **pod-side** path.
   Writing the host path here yields a domain XML pointing at a socket the VM
   cannot open.

5. **Hand the host-side path to the agent** over the existing routedcni gRPC
   API, so it can render the vSR `fpvhost` virtual-port.

### 7.2 On `cmdDel`

The device plugin owns the socket directory; we must **not** delete it. Our
teardown is limited to withdrawing the fpvhost port via the agent and removing
the CNI device-info file we wrote.

### 7.3 What we must not do

- Do not create, bind, or `chown` the socket — the plugin and vSR own it.
- Do not persist the `deviceID` anywhere durable (§5.1).
- Do not assume a stable mapping from attachment order to pod-side index without
  having tested it (§5.2).

## 8. Where the current implementation is wrong

### 8.1 `socketPath` cannot be a static NAD field

`pkg/cni/config.go` requires `socketPath` and the reference NAD sets:

```json
"socketPath": "/var/run/vhostuser/vhostuser-vm-net.sock"
```

No such path is ever created. The real path is allocated, is keyed by the device
ID on the host side, and lives under `/run/vsr-*`. The plugin must instead
**derive** it:

| Consumer | Runs in | Correct path |
|---|---|---|
| our CNI plugin / CRA-VSR agent | host mount namespace | `/run/vsr-vhost-user/<deviceID>/socket` |
| the KubeVirt hook / the VM domain | pod mount namespace | `/run/vsr-virtio-user/<index>/socket` |

(shown for a pod requesting `virtio-user`; the trees swap for `vhost-user`)

Writing the host path into the device-info file consumed by KubeVirt — or
configuring the vSR fpvhost port with the pod-side path — will produce a socket
that silently never connects. Suggested handling: keep `socketPath` as an
optional **override** for non-device-plugin setups, and derive it from the
`deviceID` whenever one is present.

### 8.2 The device-info file is already staged for us

The plugin writes a device-info file under `devinfo/dp/`; our plugin writes its
own under `devinfo/cni/`. Both will exist — but the important part is that
**Multus copies the former into the latter before calling us** (§3, step 6),
whenever the NAD requests the `CNIDeviceInfoFile` capability.

So the current implementation's habit of populating our file purely from static
NAD config is not just wrong, it is also more work than necessary: the correct
content is usually already sitting at the path we were handed. Read it, use it,
and only fall back to derivation if it is absent — the copy is best-effort and
Multus ignores failures.

### 8.3 `socketMode` is a claim we have not checked

The NAD hardcodes `socketMode: server` and the code comments assert *"pod server
=> VSR client"*. The plugin's own device-info file also carries a `mode`. If the
two disagree, the fpvhost port will be rendered with the wrong polarity and no
connection will be established. Whichever we choose, **the plugin's file is the
authority** and should win. Marked **[unverified]** deliberately — this is the
single most likely thing to be wrong.

### 8.4 The vSR container must be able to see the sockets

Because the socket is a **filesystem path** rather than a netdev, it cannot be
moved into a namespace: both ends must share the directory. The CRA vSR
container therefore needs `/run/vsr-vhost-user` and `/run/vsr-virtio-user`
bind-mounted from the host. On the T-CaaS image side this is now handled by the
CRA startup script (`hbn_node_deps`), gated behind the fast path. A vSR without
those mounts will create sockets that are invisible to every workload on the
node.

Mount propagation is **not** required — only sockets and ordinary directories
appear in those trees, never new mountpoints, so a plain bind mount is
sufficient **[established]**.

## 9. End-to-end picture

```
  ┌──────────────────────── node ────────────────────────┐
  │                                                       │
  │  6WIND device plugin                                  │
  │    • advertises  nc-k8s-plugin.6wind.com/virtio-user  │
  │    • on Allocate: mkdir, mount, env, devinfo/dp/*.json│
  │                       │                               │
  │                       ▼                               │
  │  kubelet ── injects mount + HNA_*_DEVICES env ──▶ pod │
  │             (triggered by the pod's resource request) │
  │                       │                               │
  │  Multus                                               │
  │    • NAD annotation k8s.v1.cni.cncf.io/resourceName   │
  │    • looks up allocated deviceIDs for the pod         │
  │    • picks DeviceIDs[Index], Index++ per attachment   │
  │    • injects "deviceID" into our CNI config           │
  │    • copies devinfo/dp/*.json ──▶ devinfo/cni/*.json  │
  │                       │                               │
  │                       ▼                               │
  │  cni-workload  ── reads deviceID + staged devinfo       │
  │      │         ── HOST path ──▶ agent                 │
  │      │         ── POD path  ──▶ devinfo/cni/*.json    │
  │      ▼                                                │
  │  CRA-VSR agent ── NETCONF ──▶ vSR fpvhost port        │
  │                                     │                 │
  │        /run/vsr-vhost-user/<id>/socket  ◀── shared ───┤
  │                                     │                 │
  └─────────────────────────────────────┼─────────────────┘
                                        ▼
                              KubeVirt hook attaches
                              /run/vsr-virtio-user/<idx>/socket
                              to the domain XML
```

The division of labour: **the device plugin supplies the rendezvous point, we
supply everything that makes it a network.**

## 10. To verify on real hardware

In rough priority order — the first three can each independently break the
datapath:

1. **`mode` semantics.** What the plugin writes for `vhost-user` versus
   `virtio-user`, and whether our fpvhost polarity matches. (§8.3)
2. **Two attachments in one pod.** Multus hands out device IDs by cursor in
   annotation order (§3, step 4) and the plugin numbers pod-side indices per
   `Allocate()` (§5.2). Confirm the two orderings actually agree, and that they
   are stable across pod restarts. **This is the most likely source of a subtle,
   intermittent mis-wiring.**
3. **Whether Multus's device-info copy actually lands.** It is best-effort and
   failures are only logged (§3, step 6). Confirm the file is present at the
   handed-to path in practice, since §7.1 treats it as the primary source.
4. **Directory ownership.** The plugin appears to chown the socket directories
   to a fixed numeric uid/gid (`107`, consistent with a `qemu` user)
   **[inferred]**. Whether a non-root workload can actually connect needs
   checking.
5. **Whether the socket must pre-exist** before the vSR fpvhost port is
   configured, i.e. whether there is an ordering constraint between the CNI ADD
   and the NETCONF render.
6. **Behaviour on plugin restart** with pods still running — whether device IDs
   are re-derived and existing mounts go stale.

## 11. Summary for the CNI implementer

- **`deviceID` is the input.** It arrives as a top-level field in our CNI config,
  injected by Multus from the NAD's `resourceName` annotation plus the pod's
  resource request. If it is empty, fail loudly.
- **Prefer the staged device-info file** that Multus copies from the device
  plugin; derive from `deviceID` only as a fallback.
- **Host path ≠ pod path.** Give the host path to the agent, the pod path to the
  device-info file we publish.
- **Never hardcode a socket path**, and never persist a `deviceID`.
- **The plugin creates no network state at all** — the fpvhost port is ours to
  render, and the socket directories must be shared into the vSR container.
