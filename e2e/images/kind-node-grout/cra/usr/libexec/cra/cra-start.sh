#!/bin/bash
# Start the CRA container (grout flavour, nerdctl runtime).
#
# grout overlay: the grout branch of the production cra-start.sh, modelled on the
# vSR overlay cra-start.sh (nerdctl, netns cra) plus the validated grout PoC
# (start-grout.sh / hbn-datapath.sh). Differences from vSR:
#   - grout is a DPDK graph router: it needs hugepages, /dev/net/tun (net_tap),
#     and (prod) /dev/vfio for the PCIe uplinks.
#   - the node<->grout trunk `hbn` is a grout net_tap; after grout is up we move
#     that tap's kernel netdev to the HOST (grout cannot adopt a moved-in veth).
#
# The grout image ref is written to /etc/cra/image (loaded into containerd
# namespace "hbr" by the node image build/load-cra-image.sh). An optional
# /etc/cra/grout-base.init holds extra node-scoped grcli lines (VTEP loopback,
# underlay VRF, uplink ports) rendered by the config generator; it is applied
# line-by-line here.

set -euo pipefail

CRA_CIDFILE="/run/cra/.dockerid"
# Must stay well above grout's EAL heap (docker/grout.env pins it to 6144 MB):
# the heap is claimed up front, so a limit equal to it leaves nothing for the
# rest of the container and the kernel OOM-kills grout during startup. The gap
# also has to absorb everything allocated outside the heap, and that grows with
# the workload: every workload port adds its own 16 KiB-mbuf pools and queues.
# With 8192m a node reached the limit on its first workload port and grout died
# with SIGSEGV mid-creation -- losing the whole datapath, ports and uplinks
# included, which nothing rebuilds until the CRA is restarted. The limit is a
# ceiling, not a reservation (a configured node measures ~2 GiB resident), so
# the extra headroom costs nothing on the lab host.
CRA_MEMORY="${CRA_MEMORY:-12288m}"
# CPUs the CRA container may run on (see the --cpuset-cpus argument below).
#
# grout runs one datapath thread per CPU in this set beyond the first, so the
# set has to leave CPUs behind for everything else on the host: the kubelet,
# etcd, the API server, the CNIs and the test binary all share it. A fixed `0-3`
# does that on a 16-core lab host, but on a 4-core CI runner it is the entire
# machine, and three CRAs between them then own every CPU. That starves the
# cluster rather than the datapath, and it does not look like a CPU problem:
# liveness probes to *localhost* time out, etcd answers with "request timed
# out", multus gives up on the API server after a minute, and the whole lab
# takes an hour to come up.
#
# The other half of that fix is `--adaptive-irq` (see docker/grout.env), which
# stops an idle worker from spinning at all. This bounds how much a *busy* one
# can take.
#
# So take the top CPUs and leave the bottom one to the host, capped at 4. The
# cap is what the fixed value was really protecting: the lab's traffic needs a
# couple of datapath threads, and handing grout sixteen of them on a big server
# buys nothing while making the lab behave differently there than on a laptop.
# Taking the top leaves CPU 0, which the kernel favours for softirqs and timers,
# free of any datapath thread.
#
# Only one CPU is given back, not half of them. grout spends the *first* CPU of
# the set on its control plane and only the rest become datapath workers, so a
# two-CPU set leaves a single worker -- and on a four-CPU runner halving the set
# did exactly that, after which grout stopped forwarding within a minute of
# finishing its own start-up and the node lost the CRA transfer net. Reserving
# one CPU keeps two workers on that runner, which is the layout grout is
# normally run in, while still keeping every datapath thread off CPU 0.
#
# The set is built from Cpus_allowed_list rather than from nproc, because
# --cpuset-cpus takes CPU *numbers* and nproc reports a *count*. Those agree
# only when the allowed set happens to start at 0 and be contiguous. A kind node
# is a container and may well be pinned to, say, 4-7, where a count-derived
# "2-3" is both wrong and outside what the cgroup permits.
if [[ -z "${CRA_CPUS:-}" ]]; then
    _allowed=$(awk '/^Cpus_allowed_list:/ {print $2}' /proc/self/status 2>/dev/null)
    _cpus=()
    IFS=',' read -r -a _ranges <<< "${_allowed:-0-3}"
    for _range in "${_ranges[@]}"; do
        if [[ "$_range" == *-* ]]; then
            for ((_c = ${_range%-*}; _c <= ${_range#*-}; _c++)); do _cpus+=("$_c"); done
        else
            _cpus+=("$_range")
        fi
    done
    _ncra=$(( ${#_cpus[@]} - 1 ))
    (( _ncra > 4 )) && _ncra=4
    # grout spends the first CPU of the set on its control plane, so a one-CPU
    # set leaves no datapath worker at all -- it logs "running control and
    # datapath on the same CPU" and shares one. Give a two-CPU machine both of
    # its CPUs rather than half a datapath; there is nothing to reserve there.
    (( _ncra < 2 && ${#_cpus[@]} >= 2 )) && _ncra=2
    (( _ncra < 1 )) && _ncra=1
    CRA_CPUS=$(IFS=','; echo "${_cpus[*]: -_ncra}")
    unset _allowed _cpus _ranges _range _c _ncra
fi
CRA_EXTRA_ARGS="${CRA_EXTRA_ARGS:-}"
CRA_EXTRA_ARGS_ARRAY=()
[[ -n "$CRA_EXTRA_ARGS" ]] && read -r -a CRA_EXTRA_ARGS_ARRAY <<< "$CRA_EXTRA_ARGS"
CRA_IMAGE_FILE="/etc/cra/image"
CRA_NETNS="/var/run/netns/cra"
CRA_CERT_DIR="/etc/cra/certs"
CRA_FRR_CONF="/etc/cra/frr.conf"
GROUT_BASE_INIT="/etc/cra/grout-base.init"
# Port MTUs, matching the FRR flavour: 1500 towards the node on the `hbn` access
# trunk, 9100 on the fabric uplinks so the overlay still has room for the VXLAN
# header. grout rejects anything above its --max-mtu with ERANGE, so the daemon
# is started with `--max-mtu 9216` (see docker/grout.env). Anything larger than
# the port MTU is dropped silently -- no ICMP too-big comes back through grout,
# so PMTUD cannot recover -- which is why the host end of `hbn` is pinned to
# GROUT_ACCESS_MTU as well.
GROUT_ACCESS_MTU="${GROUT_ACCESS_MTU:-1500}"
GROUT_UPLINK_MTU="${GROUT_UPLINK_MTU:-9100}"
# grout sizes a VXLAN interface's default MTU for a 1500-byte underlay (1450),
# which is below the access MTU, so an overlay left at the default blackholes
# full-size frames from the node -- and grout emits no ICMP "packet too big", so
# nothing ever recovers. Match the kernel datapath (nl.DefaultMtu) instead; the
# uplinks are wide enough to carry it with the VXLAN header on top.
GROUT_OVERLAY_MTU="${GROUT_OVERLAY_MTU:-9000}"
UPLINK_MODE_FILE="/etc/cra/uplink-mode"
UPLINKS_FILE="/etc/cra/uplinks"          # prod: one PCI address per line
VTEP_FILE="/etc/cra/vtep"                # node VTEP loopback address (v4 and/or v6)
VTEP_IFACE_FILE="/etc/cra/vtep-iface"    # grout iface that holds the VTEP address
NS="hbr"

if [[ ! -f "$CRA_IMAGE_FILE" ]]; then
    echo "ERROR: $CRA_IMAGE_FILE not found" >&2
    exit 1
fi
CRA_IMAGE=$(tr -d '[:space:]' < "$CRA_IMAGE_FILE")
[[ -n "$CRA_IMAGE" ]] || { echo "ERROR: $CRA_IMAGE_FILE is empty" >&2; exit 1; }

UPLINK_MODE="tap"
[[ -f "$UPLINK_MODE_FILE" ]] && UPLINK_MODE=$(tr -d '[:space:]' < "$UPLINK_MODE_FILE")

mkdir -p "$(dirname "$CRA_CIDFILE")" "$CRA_CERT_DIR"

# The two vhost-user socket trees are shared with the device plugin, which
# creates a directory per allocation, and with the workload, which has the other
# tree bind-mounted in. grout has to be able to open the socket inside them, so
# they are mounted into the CRA container below. No mount propagation is needed:
# nothing ever mounts *inside* the trees, the plugin only creates directories.
# They are created here because the CRA usually starts before the plugin, and a
# missing bind source would otherwise appear as an empty private directory in
# the container while the plugin populated the real one on the host.
mkdir -p /run/vsr-vhost-user /run/vsr-virtio-user

# grcli helper: talk to the grout control socket inside the running container.
grcli() { nerdctl --namespace="$NS" exec "$(cat "$CRA_CIDFILE")" grcli "$@"; }

# Idempotent apply of a single grcli line, tolerating "exists" (mirrors the
# grout-cra sidecar's line-by-line idempotent reconcile).
# Run a grcli command, tolerating the two things that are expected during
# bootstrap.
#
# EEXIST is success: the batch is a full desired-state replay, so re-running it
# must be a no-op rather than an error.
#
# A lost connection is retried. grout can restart underneath us mid-bootstrap
# (it has been seen to crash once on startup and come back healthy), and every
# grcli command in this script is create-only and idempotent, so replaying one
# is safe. Without this a single restart aborts cra.service, which fails the
# whole harness even though the node would have converged moments later.
grcli_try() {
    local out attempt
    for attempt in $(seq 1 30); do
        if out=$(grcli "$@" 2>&1); then
            return 0
        fi
        if grep -qiE 'exists|eexist' <<<"$out"; then
            return 0
        fi
        if grep -qiE 'gr_connect|connection (reset|refused)|broken pipe' <<<"$out"; then
            [[ "$attempt" == 1 ]] && echo "grcli $*: grout unreachable, retrying" >&2
            sleep 2
            continue
        fi
        echo "grcli $* failed: $out" >&2
        return 1
    done
    echo "grcli $* failed: grout stayed unreachable" >&2
    return 1
}

# Already running?
if [[ -f "$CRA_CIDFILE" ]]; then
    CONTAINER_ID=$(cat "$CRA_CIDFILE")
    if nerdctl --namespace="$NS" ps -q --no-trunc | grep -q "^${CONTAINER_ID}$"; then
        echo "CRA grout container already running"
        exit 0
    fi
fi

# Remove any stale (crashed/exited) container holding the `cra-grout` name so a
# systemd restart doesn't fail with a nerdctl name-store conflict.
nerdctl --namespace="$NS" rm -f cra-grout >/dev/null 2>&1 || true
rm -f "$CRA_CIDFILE"

# vfio device mount (prod PCIe uplinks); harmless if absent (lab).
VFIO_ARGS=()
[[ -d /dev/vfio ]] && VFIO_ARGS+=(--device /dev/vfio)
# Launch the real CRA grout image in netns cra and let its default CMD start
# /sbin/init. systemd then starts grout.service (`grout -t -m 666` from
# docker/grout.env), grout-cra.service and FRR/dplane_grout. /run is deliberately
# an in-container tmpfs, not a host bind mount: grout-cra runs inside this same
# container and reaches /run/grout.sock locally, while host bootstrap still uses
# `nerdctl exec ... grcli` to talk to the in-container socket. /etc/cra is still
# backed by the host cert directory so grout-cra's generated mTLS cert/key are
# visible to the hostNetwork agent-cra-grout DaemonSet, matching the FRR e2e CRA
# contract.
nerdctl --namespace="$NS" run \
    --detach \
    --name cra-grout \
    --network="ns:${CRA_NETNS}" \
    --privileged \
    --cgroupns=host \
    --memory "$CRA_MEMORY" \
    `# grout runs one busy-polling datapath thread per CPU in this set (see the` \
    `# CRA_CPUS derivation above for how it is sized). This is purely about CPU:` \
    `# grout's memory is bounded by the EAL heap size in docker/grout.env, not` \
    `# by the thread count.` \
    --cpuset-cpus "$CRA_CPUS" \
    --hostname "grout-$(hostname)" \
    -e container=docker \
    --device /dev/net/tun \
    "${VFIO_ARGS[@]}" \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    -v /dev/hugepages:/dev/hugepages \
    -v /run/vsr-vhost-user:/run/vsr-vhost-user \
    -v /run/vsr-virtio-user:/run/vsr-virtio-user \
    -v "${CRA_CERT_DIR}:/etc/cra" \
    `# The CRA's FRR needs the underlay config the harness/config generator` \
    `# rendered for this node. Without it FRR boots with an empty frr.conf --` \
    `# it starts, reports active, and simply has no BGP instance, so the node's` \
    `# fabric sessions never leave Idle with nothing logged as an error.` \
    -v "${CRA_FRR_CONF}:/etc/frr/frr.conf" \
    --tmpfs /run \
    --tmpfs /run/lock \
    --tmpfs /tmp \
    --stop-signal SIGRTMIN+3 \
    --entrypoint /sbin/init \
    --cidfile "$CRA_CIDFILE" \
    "${CRA_EXTRA_ARGS_ARRAY[@]}" \
    "$CRA_IMAGE"

# Wait for the grout control socket to answer. First boot can be slow (image
# snapshot unpack + DPDK EAL init) and e2e-up runs this under heavy CPU
# contention, so allow a generous window; but fail fast if the container itself
# has exited (a real crash, not a slow start).
CID=$(cat "$CRA_CIDFILE")

# Wait for the container's systemd (PID 1) to accept `exec`, then explicitly
# (re)start the grout stack instead of relying solely on systemd auto-start:
# under cold-boot CPU contention grout.service's ExecStartPost could previously
# time out and get the unit torn down + restart-rate-limited to a dead state.
# reset-failed clears any such limit; --no-block kicks the units so the grcli
# poll below observes them coming up.
for _ in $(seq 1 30); do
    nerdctl --namespace="$NS" exec "$CID" systemctl is-system-running >/dev/null 2>&1 && break
    nerdctl --namespace="$NS" exec "$CID" true >/dev/null 2>&1 && break
    sleep 1
done
nerdctl --namespace="$NS" exec "$CID" systemctl reset-failed grout.service grout-cra.service frr.service >/dev/null 2>&1 || true
# frr.service is deliberately NOT started here: it is started once the datapath
# bootstrap below has created the uplinks (see the end of this script).
nerdctl --namespace="$NS" exec "$CID" systemctl start --no-block grout.service grout-cra.service >/dev/null 2>&1 || true

# Wait for grout's control socket to answer. Used before the first bootstrap
# attempt and again before every replay: grout takes on the order of ten seconds
# to come back after a restart, so replaying immediately just fails against a
# socket that is not listening yet.
wait_grout_ready() {
    local _
    for _ in $(seq 1 "${1:-150}"); do
        if grcli interface show >/dev/null 2>&1; then return 0; fi
        if ! nerdctl --namespace="$NS" ps -q --no-trunc | grep -q "^${CID}$"; then
            echo "ERROR: cra-grout container exited during startup" >&2
            nerdctl --namespace="$NS" logs cra-grout 2>&1 | tail -30 >&2 || true
            exit 1
        fi
        sleep 1
    done
    return 1
}

wait_grout_ready 150 || { echo "ERROR: grout did not become ready" >&2; exit 1; }

# ── Node-scoped base datapath ────────────────────────────────────────────────
# This is a full desired-state replay, not an incremental step: every command is
# create-only and tolerates EEXIST, so running it twice is a no-op.
#
# It has to be replayable as a whole, because grout holds all of this in memory.
# If grout restarts partway through -- it has been seen to crash once during
# startup and come back healthy -- everything applied before the restart is gone
# with it. Retrying only the command that happened to fail would leave the node
# missing the ports created earlier, which is exactly how `hbn` went missing on
# a node whose grout restarted during the uplink setup: the CRA reported success
# and the harness then waited for a trunk that no longer existed.
bootstrap_datapath() {
    # ── Node-scoped base datapath ────────────────────────────────────────────────
    # grout keeps the underlay in its default routing context; the per-tenant EVPN
    # VRFs/VXLAN/routes are programmed by FRR (dplane_grout) and the cra-grout
    # agent. Node-setup only lays down the fabric-facing ports, the node
    # VTEP address, and the host `hbn` trunk.
    #
    # VRFs are the one exception: dplane_grout disables the kernel namespace, so
    # zebra only ever learns the VRFs that exist in grout. A `vrf <name>` stanza
    # in frr.conf whose grout VRF is missing stays "not active" forever and its
    # L3VNI is never programmed. Create every VRF the rendered frr.conf mentions
    # before FRR reads it.
    if [[ -f "$CRA_FRR_CONF" ]]; then
        while read -r vrf; do
            # Tenant VRFs carry a handful of routes, not a fabric-sized table.
            # grout allocates every FIB up front out of its EAL heap, and the
            # default (65536 routes, 256/262144 tbl8 entries per family) is large
            # enough that two extra VRFs exhaust the heap and every later port
            # allocation fails with ENOMEM.
            grcli_try interface add vrf "$vrf" \
                rib4-routes 8192 fib4-tbl8 64 rib6-routes 8192 fib6-tbl8 1024 || true
        done < <(sed -n 's/^vrf \([A-Za-z0-9_.-]\+\)$/\1/p' "$CRA_FRR_CONF" | sort -u)
    fi

    # hbn trunk: grout presents a net_tap for the `hbn` port; its kernel netdev is
    # then moved to the host so kubelet/pods reach grout over host `hbn` (the CNI
    # moves per-pod net_taps the same way — see pkg/cni/grouttap.go).
    #
    # The tap's kernel name must differ from the grout port name: grout creates a
    # control plane representor tap named after the port, and a name clash makes
    # that representor's TUNSETIFF fail, after which the first punted packet
    # SEGVs grout. Mirrors workloadcni.TapIfaceName; the netdev is renamed back to
    # `hbn` as it moves to the host, so the host-facing name is unchanged.
    # hbn is the node's way into the fabric, so it belongs to the cluster VRF --
    # the same place the FRR CRA's hbn veth is enslaved to. Ports take their VRF
    # at creation time, which is why the VRFs are created first above.
    # MTU has to be set here, while the tap is still grout's: once the netdev is
    # moved to the host, grout can no longer touch it and `interface set port hbn
    # mtu` fails with EPERM. GROUT_ACCESS_MTU and the host-side MTU below have to
    # stay in lockstep -- the node silently blackholes anything larger than
    # grout's port MTU, and no ICMP too-big comes back to tell it.
    # `promisc on` has to be set here for the same reason as the MTU, and it is
    # not optional: attaching a VLAN sub-interface of a port to a bridge domain
    # makes grout turn promiscuous mode on for the parent port, and by then the
    # netdev is the host's, so the driver call fails with EPERM and every L2 the
    # operator maps onto the trunk is rejected. Enabling it up front takes
    # grout's promisc refcount to 1, so the later bridge attach only increments
    # it and never touches the driver.
    grcli_try interface add port hbn devargs net_tap0,iface=hbn_dp mtu "$GROUT_ACCESS_MTU" promisc on vrf cluster
    # Enable unsolicited router advertisements on the trunk. The node's default
    # route is static (netplan), but Linux merges it into the RA-learned entry
    # for the same prefix as soon as one solicited RA arrives, and the whole
    # merged entry -- static nexthop included -- is dropped when the RA lifetime
    # runs out. grout answers router solicitations but sends nothing
    # periodically unless RAs are switched on, so the node lost its default
    # route (and with it image pulls and DNS) exactly 30 minutes after boot.
    # The FRR flavour has zebra sending periodic RAs on the trunk instead.
    grcli_try router-advert set hbn || true

    for _ in $(seq 1 30); do ip -n cra link show hbn_dp >/dev/null 2>&1 && break; sleep 0.5; done
    if ip -n cra link show hbn_dp >/dev/null 2>&1; then
        ip -n cra link set hbn_dp netns 1 name hbn
        # DPDK gives the tap netdev the same MAC as the grout port it belongs to.
        # On a point-to-point link that is fatal: grout's neighbour solicitations
        # for the node are answered with grout's own address, so both nexthops
        # stayed unresolved and nothing the node sent was ever answered. Give the
        # host end an address of its own (locally administered, and only ever
        # visible on this one link).
        ip link set hbn address 02:00:00:00:00:01 || true
        ip link set hbn up mtu "$GROUT_ACCESS_MTU" || true
    else
        echo "Warning: grout net_tap hbn_dp did not appear in netns cra" >&2
    fi

    # Fabric uplinks.
    if [[ "$UPLINK_MODE" == "vfio" && -f "$UPLINKS_FILE" ]]; then
        # prod: bind each vfio-pci PCIe NIC as a grout DPDK physical port.
        idx=1
        while IFS= read -r pci; do
            [[ -z "$pci" || "$pci" =~ ^[[:space:]]*# ]] && continue
            grcli_try interface add port "uplink${idx}" devargs "$pci" mtu "$GROUT_UPLINK_MTU"
            idx=$((idx + 1))
        done < "$UPLINKS_FILE"
    else
        # lab: grout presents a net_tap per uplink and we bridge it (in netns cra) to
        # the moved-in fabric veth so grout reaches the containerlab fabric. NEEDS
        # LIVE VALIDATION on a DPDK-capable lab host.
        # Ports are created here but attached to the fabric only at the very
        # end of the bootstrap, once grout is fully configured.
        #
        # Bridging as we go put the node in a state where fabric traffic (the
        # leaves' router advertisements arrive immediately) was already flowing
        # into grout while the next port was still being created, and grout
        # reproducibly died there -- "write to tap device failed" from the
        # control plane punt path, then a SEGV. It restarts, loses the ports
        # created so far, and the node never converges.
        #
        # Creating the ports while nothing is attached keeps the datapath quiet
        # until it is fully set up.
        idx=1
        if [[ -f /etc/cra/interfaces ]]; then
            while IFS= read -r veth; do
                [[ -z "$veth" || "$veth" =~ ^[[:space:]]*# ]] && continue
                grcli_try interface add port "uplink${idx}" devargs "net_tap$((idx)),iface=up${idx}" mtu "$GROUT_UPLINK_MTU"
                idx=$((idx + 1))
            done < /etc/cra/interfaces
        fi
    fi

    # Node VTEP address: the source IP for every `vxlan ... local <vtep>` the agent
    # and FRR dplane later program, so it must exist on a grout interface first.
    # grout has no default loopback (loopback interface support is still maturing
    # upstream), so the VTEP is assigned to a real grout interface — by default the
    # first fabric uplink port (matching grout's EVPN smoke tests, which put the
    # VTEP on the underlay port). Override via /etc/cra/vtep-iface (e.g. a grout
    # `loopback` once available, for a /32 VTEP over ECMP uplinks).
    VTEP_IFACE="uplink1"
    [[ -f "$VTEP_IFACE_FILE" ]] && VTEP_IFACE=$(tr -d '[:space:]' < "$VTEP_IFACE_FILE")
    VTEP_V4=""
    if [[ -f "$VTEP_FILE" ]]; then
        while IFS= read -r vtep; do
            [[ -z "$vtep" || "$vtep" =~ ^[[:space:]]*# ]] && continue
            # grcli requires a prefix length; a bare address is rejected outright.
            # The VTEP is a host address in both families.
            if [[ "$vtep" != */* ]]; then
                [[ "$vtep" == *:* ]] && vtep="${vtep}/128" || vtep="${vtep}/32"
            fi
            # The VXLAN interfaces below need the bare v4 VTEP as their source.
            [[ "$vtep" != *:* && -z "$VTEP_V4" ]] && VTEP_V4="${vtep%%/*}"
            grcli_try address add "$vtep" iface "$VTEP_IFACE" || true
        done < "$VTEP_FILE"
    fi

    # L3VNI VXLAN interface per tenant VRF. `vrf cluster / vni 30` in frr.conf only
    # tells zebra which VNI belongs to which VRF; the VXLAN interface that carries
    # it has to exist in the datapath. The FRR CRA gets one from its own base
    # config; grout's does not, so `show evpn vni` listed both L3VNIs with
    # "VxLAN IF None" and no EVPN type-5 route was ever imported into the VRF.
    if [[ -f "$CRA_FRR_CONF" && -n "$VTEP_V4" ]]; then
        while read -r vrf vni; do
            grcli_try interface add vxlan "vxlan${vni}" vni "$vni" local "$VTEP_V4" \
                vrf "$vrf" mtu "$GROUT_OVERLAY_MTU" || true
        done < <(awk '/^vrf [A-Za-z0-9_.-]+$/ { v=$2 } /^ vni [0-9]+$/ && v != "" { print v, $2; v="" }' "$CRA_FRR_CONF")
    fi

    # Apply any extra node-scoped grcli lines from the config generator.
    if [[ -f "$GROUT_BASE_INIT" ]]; then
        while IFS= read -r line; do
            line="${line#"${line%%[![:space:]]*}"}"   # ltrim
            [[ -z "$line" || "$line" == \#* ]] && continue
            # shellcheck disable=SC2086
            grcli_try $line || true
        done < "$GROUT_BASE_INIT"
    fi

    # Attach the fabric last, once every port, address and base-init line is in
    # place.
    #
    # Bridging as each port was created put the node in a state where fabric
    # traffic -- the leaves' router advertisements arrive immediately -- was
    # already flowing into grout while the rest of the datapath was still being
    # built, and grout reproducibly died there: "write to tap device failed"
    # from the control plane punt path, then a SEGV. It restarts, loses
    # everything created so far, and the node never converges.
    #
    # Keeping the datapath quiet until it is fully configured avoids that
    # window entirely.
    if [[ "$UPLINK_MODE" != "vfio" && -f /etc/cra/interfaces ]]; then
        local idx=1
        while IFS= read -r veth; do
            [[ -z "$veth" || "$veth" =~ ^[[:space:]]*# ]] && continue
            for _ in $(seq 1 30); do ip -n cra link show "up${idx}" >/dev/null 2>&1 && break; sleep 0.5; done
            if ip -n cra link show "up${idx}" >/dev/null 2>&1; then
                ip -n cra link add "bru${idx}" type bridge 2>/dev/null || true
                # The DPDK tap and the bridge are dumb wires into grout: they must
                # not carry IPv6. They inherit grout's port MAC, so the kernel
                # derives the SAME link-local on them that grout puts on the port's
                # control plane representor -- one address on three netdevs. The
                # node's own stack then answers for it: it completed the leaf's BGP
                # TCP handshake on the tap instead of letting the packet reach the
                # representor, and bgpd dropped the connection because it arrived
                # on an interface it does not know ("Could not get instance for
                # incoming conn"). Disable IPv6 before the links come up, so the
                # address is never generated in the first place.
                ip netns exec cra sysctl -qw "net.ipv6.conf.bru${idx}.disable_ipv6=1" || true
                ip netns exec cra sysctl -qw "net.ipv6.conf.up${idx}.disable_ipv6=1" || true
                # Give the tap a MAC of its own. DPDK mirrors grout's port MAC
                # onto the kernel netdev, and a Linux bridge treats a frame
                # addressed to one of its ports' own MACs as destined to the
                # bridge itself: it delivers it locally instead of forwarding it
                # out that port. Every unicast frame for grout was therefore
                # swallowed by bru${idx} and never reached the datapath, while
                # multicast (the leaves' router advertisements) was flooded
                # through -- so the peers discovered each other over RAs and then
                # sat in Connect forever, because not one BGP SYN ever arrived.
                # The tap is only a wire: grout keeps its own MAC on the port and
                # still receives everything the kernel transmits here.
                ip -n cra link set "up${idx}" address \
                    "$(printf '02:00:00:00:%02x:01' "$idx")" || true
                ip -n cra link set "$veth" master "bru${idx}" || true
                ip -n cra link set "up${idx}" master "bru${idx}" || true
                ip -n cra link set "bru${idx}" up || true
                ip -n cra link set "up${idx}" up || true
            fi
            idx=$((idx + 1))
        done < /etc/cra/interfaces
    fi
}

# Number of fabric uplinks this node should end up with, from whichever source
# the bootstrap uses to create them.
if [[ "$UPLINK_MODE" == "vfio" && -f "$UPLINKS_FILE" ]]; then
    UPLINK_COUNT=$(grep -cvE '^[[:space:]]*(#|$)' "$UPLINKS_FILE" || true)
elif [[ -f /etc/cra/interfaces ]]; then
    UPLINK_COUNT=$(grep -cvE '^[[:space:]]*(#|$)' /etc/cra/interfaces || true)
else
    UPLINK_COUNT=0
fi

# The state the bootstrap must have left behind. Checked after each attempt so a
# restart mid-bootstrap is caught here rather than surfacing later as a missing
# interface.
#
# It has to cover everything bootstrap_datapath creates, not just the ports: the
# individual grcli calls tolerate failure so a replay can be idempotent, so this
# is the only thing standing between a half-programmed datapath and a CRA that
# reports success. Checking the ports alone let a grout restart during the second
# half of the bootstrap through -- the L3VNI VXLAN interfaces and the node's
# transfer routes were simply absent, FRR came up, EVPN sessions established, and
# the node silently had no path anywhere.
bootstrap_complete() {
    local out addrs idx vrf vni line dest rest

    out=$(grcli interface show 2>/dev/null) || return 1
    grep -qw hbn <<<"$out" || return 1
    idx=1
    while [[ $idx -le "$UPLINK_COUNT" ]]; do
        grep -qw "uplink${idx}" <<<"$out" || return 1
        idx=$((idx + 1))
    done

    # Every VRF the rendered frr.conf declares, and the VXLAN interface carrying
    # each of its L3VNIs.
    if [[ -f "$CRA_FRR_CONF" ]]; then
        while read -r vrf; do
            [[ -z "$vrf" ]] && continue
            grep -qw "$vrf" <<<"$out" || return 1
        done < <(sed -n 's/^vrf \([A-Za-z0-9_.-]\+\)$/\1/p' "$CRA_FRR_CONF" | sort -u)
        while read -r vrf vni; do
            [[ -z "$vni" ]] && continue
            grep -qw "vxlan${vni}" <<<"$out" || return 1
        done < <(awk '/^vrf [A-Za-z0-9_.-]+$/ { v=$2 } /^ vni [0-9]+$/ && v != "" { print v, $2; v="" }' "$CRA_FRR_CONF")
    fi

    addrs=$(grcli address show 2>/dev/null) || return 1

    # The VTEP address, which every `vxlan ... local <vtep>` is sourced from.
    if [[ -f "$VTEP_FILE" ]]; then
        while IFS= read -r line; do
            line="${line%%#*}"
            line=$(tr -d '[:space:]' <<<"$line")
            [[ -z "$line" ]] && continue
            grep -qF "${line%%/*}" <<<"$addrs" || return 1
        done < "$VTEP_FILE"
    fi

    # The node-scoped addresses and routes from the config generator: the host
    # transfer net and the routes back to the node's own addresses.
    if [[ -f "$GROUT_BASE_INIT" ]]; then
        while IFS= read -r line; do
            line="${line#"${line%%[![:space:]]*}"}"   # ltrim
            [[ -z "$line" || "$line" == \#* ]] && continue
            # shellcheck disable=SC2086
            set -- $line
            case "$1 $2" in
            "address add")
                grep -qF "${3%%/*}" <<<"$addrs" || return 1
                ;;
            "route add")
                dest="$3"
                rest=""
                [[ "$line" == *" vrf "* ]] && rest="vrf ${line##* vrf }"
                # shellcheck disable=SC2086
                grcli route show $rest 2>/dev/null | grep -qF "$dest" || return 1
                ;;
            esac
        done < "$GROUT_BASE_INIT"
    fi
}

for attempt in $(seq 1 8); do
    bootstrap_datapath || true
    if bootstrap_complete; then
        break
    fi
    if [[ "$attempt" == 8 ]]; then
        echo "ERROR: grout datapath bootstrap did not converge" >&2
        exit 1
    fi
    echo "grout state incomplete (it likely restarted); replaying node setup" >&2
    wait_grout_ready 60 || true
done

# Only now start FRR. bgpd asks zebra to enable IPv6 router advertisements on
# each unnumbered peer interface exactly once, as it reads its config; if the
# grout uplinks do not exist yet zebra answers "IF 0 RA enable client bgp -
# interface unknown" and never sends an RA, so the peer never learns our
# link-local and every unnumbered session sits in Idle forever. Starting FRR
# after the uplinks exist is also what grout's own BGP unnumbered smoke test
# does. restart (not start) so a replayed bootstrap -- which recreates the
# interfaces with fresh ifindexes -- re-syncs FRR with them.
nerdctl --namespace="$NS" exec "$CID" \
    sh -c 'mkdir -p /run/cra && touch /run/cra/datapath-ready' >/dev/null 2>&1 || true
nerdctl --namespace="$NS" exec "$CID" systemctl restart --no-block frr.service >/dev/null 2>&1 || true

# Verify the node-scoped grcli lines are in place once FRR is running, and
# re-apply whatever is missing. The datapath is built asynchronously -- grout can
# still be finishing a port, and a bootstrap replay recreates interfaces with
# fresh ifindexes -- so this is a convergence check, not a workaround: FRR itself
# was verified not to remove grout FIB entries it does not know about, neither on
# its first start nor across a restart. The lines are idempotent, so re-applying
# them costs nothing when nothing was lost.
if [[ -f "$GROUT_BASE_INIT" ]]; then
    for _ in $(seq 1 30); do
        bootstrap_complete && sleep 3 && bootstrap_complete && break
        while IFS= read -r line; do
            line="${line#"${line%%[![:space:]]*}"}"   # ltrim
            [[ -z "$line" || "$line" == \#* ]] && continue
            # shellcheck disable=SC2086
            grcli_try $line || true
        done < "$GROUT_BASE_INIT"
        sleep 2
    done
fi

echo "CRA grout container started: $CRA_IMAGE (memory: $CRA_MEMORY, cpus: $CRA_CPUS, uplink: $UPLINK_MODE)"
exit 0
