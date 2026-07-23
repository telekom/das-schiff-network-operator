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
CRA_MEMORY="${CRA_MEMORY:-2048m}"
CRA_EXTRA_ARGS="${CRA_EXTRA_ARGS:-}"
CRA_EXTRA_ARGS_ARRAY=()
[[ -n "$CRA_EXTRA_ARGS" ]] && read -r -a CRA_EXTRA_ARGS_ARRAY <<< "$CRA_EXTRA_ARGS"
CRA_IMAGE_FILE="/etc/cra/image"
CRA_NETNS="/var/run/netns/cra"
CRA_CERT_DIR="/etc/cra/certs"
GROUT_BASE_INIT="/etc/cra/grout-base.init"
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

# grcli helper: talk to the grout control socket inside the running container.
grcli() { nerdctl --namespace="$NS" exec "$(cat "$CRA_CIDFILE")" grcli "$@"; }

# Idempotent apply of a single grcli line, tolerating "exists" (mirrors the
# grout-cra sidecar's line-by-line idempotent reconcile).
grcli_try() {
    local out
    if ! out=$(grcli "$@" 2>&1); then
        if grep -qiE 'exists|eexist' <<<"$out"; then return 0; fi
        echo "grcli $* failed: $out" >&2
        return 1
    fi
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
    --hostname "grout-$(hostname)" \
    -e container=docker \
    --device /dev/net/tun \
    "${VFIO_ARGS[@]}" \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    -v /dev/hugepages:/dev/hugepages \
    -v "${CRA_CERT_DIR}:/etc/cra" \
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
nerdctl --namespace="$NS" exec "$CID" systemctl start --no-block grout.service grout-cra.service frr.service >/dev/null 2>&1 || true

ready=0
for _ in $(seq 1 150); do
    if grcli interface show >/dev/null 2>&1; then ready=1; break; fi
    if ! nerdctl --namespace="$NS" ps -q --no-trunc | grep -q "^${CID}$"; then
        echo "ERROR: cra-grout container exited during startup" >&2
        nerdctl --namespace="$NS" logs cra-grout 2>&1 | tail -30 >&2 || true
        exit 1
    fi
    sleep 1
done
[[ "$ready" == 1 ]] || { echo "ERROR: grout did not become ready" >&2; exit 1; }

# ── Node-scoped base datapath ────────────────────────────────────────────────
# Underlay: grout keeps the underlay in its default routing context; the
# per-tenant EVPN VRFs/VXLAN/routes are programmed by FRR (dplane_grout) and the
# cra-grout agent. Node-setup only lays down the fabric-facing ports, the node
# VTEP address, and the host `hbn` trunk.

# hbn trunk: grout presents a net_tap `hbn`; its kernel netdev is then moved to
# the host so kubelet/pods reach grout over host `hbn` (the CNI moves per-pod
# net_taps the same way — see pkg/cni/grouttap.go).
grcli_try interface add port hbn devargs net_tap0,iface=hbn
# Wait for the tap netdev to appear inside netns cra, then move it to the host.
for _ in $(seq 1 30); do ip -n cra link show hbn >/dev/null 2>&1 && break; sleep 0.5; done
if ip -n cra link show hbn >/dev/null 2>&1; then
    ip -n cra link set hbn netns 1
    ip link set hbn up mtu 9100 || true
else
    echo "Warning: grout net_tap hbn did not appear in netns cra" >&2
fi

# Fabric uplinks.
if [[ "$UPLINK_MODE" == "vfio" && -f "$UPLINKS_FILE" ]]; then
    # prod: bind each vfio-pci PCIe NIC as a grout DPDK physical port.
    idx=1
    while IFS= read -r pci; do
        [[ -z "$pci" || "$pci" =~ ^[[:space:]]*# ]] && continue
        grcli_try interface add port "uplink${idx}" devargs "$pci"
        idx=$((idx + 1))
    done < "$UPLINKS_FILE"
else
    # lab: grout presents a net_tap per uplink and we bridge it (in netns cra) to
    # the moved-in fabric veth so grout reaches the containerlab fabric. NEEDS
    # LIVE VALIDATION on a DPDK-capable lab host.
    idx=1
    if [[ -f /etc/cra/interfaces ]]; then
        while IFS= read -r veth; do
            [[ -z "$veth" || "$veth" =~ ^[[:space:]]*# ]] && continue
            grcli_try interface add port "uplink${idx}" devargs "net_tap$((idx)),iface=up${idx}"
            for _ in $(seq 1 30); do ip -n cra link show "up${idx}" >/dev/null 2>&1 && break; sleep 0.5; done
            if ip -n cra link show "up${idx}" >/dev/null 2>&1; then
                ip -n cra link add "bru${idx}" type bridge 2>/dev/null || true
                ip -n cra link set "$veth" master "bru${idx}" || true
                ip -n cra link set "up${idx}" master "bru${idx}" || true
                ip -n cra link set "bru${idx}" up || true
                ip -n cra link set "up${idx}" up || true
            fi
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
if [[ -f "$VTEP_FILE" ]]; then
    while IFS= read -r vtep; do
        [[ -z "$vtep" || "$vtep" =~ ^[[:space:]]*# ]] && continue
        grcli_try address add "$vtep" iface "$VTEP_IFACE" || true
    done < "$VTEP_FILE"
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

echo "CRA grout container started: $CRA_IMAGE (memory: $CRA_MEMORY, uplink: $UPLINK_MODE)"
exit 0
