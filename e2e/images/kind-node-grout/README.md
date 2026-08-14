# kind-node-grout — grout CRA node image

Self-contained kind node image for the **cra-grout** flavour (FRR control plane +
[grout](https://github.com/DPDK/grout) DPDK fast path). It is an *overlay* on the
base FRR e2e kind-node (`e2e/images/kind-node`), modelled on the hbn-hands-on
`kind-node-vsr` overlay: the real `das-schiff-cra-grout:latest` image is saved as
`e2e/images/kind-node-grout/cra-grout.tar` before the node image build, imported
into containerd namespace `hbr`, and run as a **nerdctl container** in netns
`cra`.

## Node datapath model (why grout is inverted vs. FRR/vSR)

grout owns its ports as DPDK ports and **cannot adopt a moved-in kernel veth**
(the edge image has no af_packet/af_xdp/memif PMD). So the tap direction is
inverted: grout *creates* a `net_tap`, and the kernel netdev side is moved to
wherever the peer lives.

| element        | FRR / vSR                        | grout                                             |
|----------------|----------------------------------|---------------------------------------------------|
| runtime        | nspawn machine / nerdctl         | nerdctl container in netns `cra` (`hbr` namespace) |
| `hbn` trunk    | veth peer into netns             | grout `net_tap`; netdev **moved to host**          |
| fabric uplinks | veth / PCIe `port pci-bXsY`      | vfio-pci DPDK port (prod) / net_tap+bridge (lab)   |
| per-pod port   | veth moved into pod              | grout `net_tap` moved into pod (`pkg/cni/grouttap.go`) |
| datapath prereq| —                                | hugepages, `/dev/net/tun`, `/dev/vfio` (prod)      |

## Lifecycle scripts (`cra/usr/libexec/cra/`)

- **cra-generator** — emits a `nerdctl` `cra.service` for `flavour=grout`
  (replaces the base nspawn-only generator), with a `.d/devices.conf` drop-in
  that waits for the fabric interfaces to appear.
- **cra-setup-network.sh** (`ExecStartPre`) — creates netns `cra`, moves the
  fabric uplinks in (lab), and ensures hugepages.
- **cra-start.sh** (`ExecStart`) — `nerdctl run` starts the real CRA image with
  its default `/sbin/init`; systemd starts `grout.service`, `grout-cra.service`,
  and FRR/dplane_grout. The host script waits with `nerdctl exec ... grcli`, then
  programs the node-scoped base datapath: VTEP loopback address, the `hbn`
  net_tap trunk (moved to the host), and the fabric uplinks (vfio prod / net_tap
  lab). Any extra node-scoped grcli lines in `/etc/cra/grout-base.init` are
  applied idempotently (line-by-line, tolerating "exists"). `/run` stays inside
  the container; `/etc/cra/certs` is shared as `/etc/cra` for the grout-cra mTLS
  cert/key used by the hostNetwork agent.
- **cra-stop.sh** (`ExecStop`) — `nerdctl stop/rm` the grout container.
- **cra-cleanup-network.sh** (`ExecStopPost`) — moves uplinks back, removes the
  host `hbn` trunk and lab bridges, deletes netns `cra`.
- **load-cra-image.sh** — imports a grout image into containerd `hbr` and writes
  `/etc/cra/{flavour,image}`; the normal e2e path bakes `cra-grout.tar` during
  the node-image build.

## Node config files (rendered by the config generator)

- `/etc/cra/flavour` = `grout`
- `/etc/cra/image` = `das-schiff-cra-grout:latest` (in `hbr`)
- `/etc/cra/interfaces` = fabric uplink netdevs (lab veths)
- `/etc/cra/uplink-mode` = `vfio` (prod PCIe) or `tap` (lab veth); default `tap`
- `/etc/cra/uplinks` = PCI addresses, one per line (prod `vfio` mode)
- `/etc/cra/vtep` = node VTEP loopback address(es), v4 and/or v6
- `/etc/cra/vtep-iface` = grout interface holding the VTEP (default `uplink1`)
- `/etc/cra/grout-base.init` = optional extra node-scoped grcli lines

## Status

The node now bakes and runs our real CRA image, not the raw upstream grout
daemon. The lifecycle wiring mirrors the validated vSR overlay, and the `hbn`
net_tap trunk + move-to-host and VTEP loopback follow the validated grout PoC
(`files/grout-poc/`). The **lab uplink net_tap+bridge shim**, hugepages sizing,
`/dev/vfio` prod uplinks, and systemd-in-nerdctl on `kindest/node` still need
live validation on a DPDK-capable host (covered by the `e2e-grout` task).
