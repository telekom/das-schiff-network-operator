#!/usr/bin/env bash
# grout-node-prep.sh — prepare a production hypervisor node for the grout DPDK
# fast path (cra-grout flavour). It is the "no -t" (real hugepages + vfio-pci)
# counterpart of the lab/MVP path, which runs `grout -t` (test-mode, no
# hugepages, net_tap uplinks) and needs none of this.
#
# What it does (all idempotent):
#   1. Reserves hugepages (default 2MB pages; optionally 1GB) and mounts
#      hugetlbfs so DPDK mempools don't exhaust (net_tap/net_vhost port creation
#      needs adequate mempool — test-mode `-t` no-huge exhausts after ~2 ports).
#   2. Enables the IOMMU (intel_iommu=on / amd_iommu=on iommu=pt) via GRUB so
#      PCIe uplinks can be safely assigned to userspace DPDK.
#   3. Binds the fabric uplink NIC(s) to vfio-pci so grout owns them as DPDK
#      physical ports (`interface add port uplinkN devargs <pci>`).
#   4. Optionally isolates CPUs for the DPDK poll-mode threads (isolcpus / nohz).
#
# Steps 2 and 4 change the kernel command line and require a REBOOT to take
# effect; the script prints a clear notice and does NOT reboot for you.
#
# Usage:
#   grout-node-prep.sh hugepages [--size 2M|1G] [--count N]
#   grout-node-prep.sh iommu                       # edits GRUB (reboot needed)
#   grout-node-prep.sh bind <pci-addr> [<pci-addr>...]
#   grout-node-prep.sh isolate <cpu-list>          # edits GRUB (reboot needed)
#   grout-node-prep.sh all --count N <pci-addr>... # hugepages + bind (no GRUB)
#
# Examples:
#   grout-node-prep.sh hugepages --size 1G --count 8
#   grout-node-prep.sh iommu
#   grout-node-prep.sh bind 0000:03:00.0 0000:03:00.1
#   grout-node-prep.sh isolate 4-15
set -euo pipefail

HUGE_SIZE="2M"
HUGE_COUNT="1024"
GRUB_FILE="/etc/default/grub"

need_root() { [[ "$(id -u)" -eq 0 ]] || { echo "must run as root" >&2; exit 1; }; }

# ── hugepages ────────────────────────────────────────────────────────────────
prep_hugepages() {
    local kb mnt nr_path
    case "$HUGE_SIZE" in
        2M) kb=2048;    mnt=/dev/hugepages;     nr_path=/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages ;;
        1G) kb=1048576; mnt=/dev/hugepages1G;   nr_path=/sys/kernel/mm/hugepages/hugepages-1048576kB/nr_hugepages ;;
        *)  echo "unknown --size $HUGE_SIZE (want 2M or 1G)" >&2; exit 1 ;;
    esac

    echo ">> reserving $HUGE_COUNT x $HUGE_SIZE hugepages"
    if [[ -w "$nr_path" ]]; then
        echo "$HUGE_COUNT" > "$nr_path"
    else
        echo "!! $nr_path not writable (1G pages usually need a boot-time reservation" >&2
        echo "   via GRUB: default_hugepagesz=1G hugepagesz=1G hugepages=$HUGE_COUNT)" >&2
    fi

    # Persist the 2MB reservation across reboots (1G is best done on the cmdline).
    if [[ "$HUGE_SIZE" == "2M" ]]; then
        echo "vm.nr_hugepages = $HUGE_COUNT" > /etc/sysctl.d/10-grout-hugepages.conf
    fi

    mkdir -p "$mnt"
    if ! mountpoint -q "$mnt"; then
        mount -t hugetlbfs -o "pagesize=${kb}k" nodev "$mnt"
    fi
    # Persist the mount.
    if ! grep -q "$mnt" /etc/fstab; then
        echo "nodev $mnt hugetlbfs pagesize=${kb}k 0 0" >> /etc/fstab
    fi
    grep -H "" /sys/kernel/mm/hugepages/hugepages-*/nr_hugepages
}

# ── IOMMU (GRUB cmdline; reboot needed) ──────────────────────────────────────
prep_iommu() {
    local vendor params
    vendor=$(grep -m1 -o 'GenuineIntel\|AuthenticAMD' /proc/cpuinfo || true)
    if [[ "$vendor" == "AuthenticAMD" ]]; then
        params="amd_iommu=on iommu=pt"
    else
        params="intel_iommu=on iommu=pt"
    fi
    echo ">> ensuring IOMMU kernel params: $params"
    add_grub_cmdline "$params"
    echo "!! IOMMU changes require a REBOOT to take effect."
}

# ── CPU isolation (GRUB cmdline; reboot needed) ──────────────────────────────
prep_isolate() {
    local cpus="$1"
    [[ -n "$cpus" ]] || { echo "isolate needs a cpu-list (e.g. 4-15)" >&2; exit 1; }
    echo ">> isolating CPUs $cpus for the DPDK poll-mode threads"
    add_grub_cmdline "isolcpus=${cpus} nohz_full=${cpus} rcu_nocbs=${cpus}"
    echo "!! CPU isolation changes require a REBOOT to take effect."
}

# add_grub_cmdline appends space-separated params to GRUB_CMDLINE_LINUX,
# skipping any key already present, and regenerates grub.cfg.
add_grub_cmdline() {
    local params="$1" line current key changed=0
    [[ -f "$GRUB_FILE" ]] || { echo "!! $GRUB_FILE not found; set kernel params manually: $params" >&2; return 0; }
    current=$(grep -oP '(?<=^GRUB_CMDLINE_LINUX=").*(?="$)' "$GRUB_FILE" || echo "")
    line="$current"
    for p in $params; do
        key="${p%%=*}"
        if ! grep -qE "(^| )${key}(=| |$)" <<<"$line"; then
            line="${line:+$line }$p"
            changed=1
        fi
    done
    if [[ "$changed" -eq 1 ]]; then
        if grep -q '^GRUB_CMDLINE_LINUX=' "$GRUB_FILE"; then
            sed -i "s|^GRUB_CMDLINE_LINUX=.*|GRUB_CMDLINE_LINUX=\"${line}\"|" "$GRUB_FILE"
        else
            echo "GRUB_CMDLINE_LINUX=\"${line}\"" >> "$GRUB_FILE"
        fi
        regen_grub
    else
        echo "   (kernel params already present)"
    fi
}

regen_grub() {
    if command -v update-grub >/dev/null 2>&1; then
        update-grub
    elif command -v grub2-mkconfig >/dev/null 2>&1; then
        grub2-mkconfig -o /boot/grub2/grub.cfg 2>/dev/null || \
            grub2-mkconfig -o /boot/grub/grub.cfg
    else
        echo "!! no update-grub/grub2-mkconfig found; regenerate grub.cfg manually" >&2
    fi
}

# ── vfio-pci binding ─────────────────────────────────────────────────────────
prep_bind() {
    [[ $# -ge 1 ]] || { echo "bind needs at least one PCI address" >&2; exit 1; }
    modprobe vfio-pci || { echo "!! failed to load vfio-pci (IOMMU enabled + rebooted?)" >&2; exit 1; }
    for pci in "$@"; do
        bind_one_vfio "$pci"
    done
}

bind_one_vfio() {
    local pci="$1" cur vid did
    [[ -e "/sys/bus/pci/devices/$pci" ]] || { echo "!! no such PCI device: $pci" >&2; return 1; }
    cur=$(basename "$(readlink -f "/sys/bus/pci/devices/$pci/driver" 2>/dev/null)" 2>/dev/null || echo "")
    if [[ "$cur" == "vfio-pci" ]]; then
        echo "   $pci already bound to vfio-pci"
        return 0
    fi
    echo ">> binding $pci to vfio-pci (was: ${cur:-none})"
    [[ -n "$cur" ]] && echo "$pci" > "/sys/bus/pci/devices/$pci/driver/unbind"
    vid=$(cat "/sys/bus/pci/devices/$pci/vendor")
    did=$(cat "/sys/bus/pci/devices/$pci/device")
    echo "${vid#0x} ${did#0x}" > /sys/bus/pci/drivers/vfio-pci/new_id 2>/dev/null || true
    echo "$pci" > /sys/bus/pci/drivers/vfio-pci/bind 2>/dev/null || \
        echo "vfio-pci" > "/sys/bus/pci/devices/$pci/driver_override" && \
        echo "$pci" > /sys/bus/pci/drivers_probe
    # Record for cra-start.sh (prod uplink-mode reads /etc/cra/uplinks).
    mkdir -p /etc/cra
    grep -qxF "$pci" /etc/cra/uplinks 2>/dev/null || echo "$pci" >> /etc/cra/uplinks
    echo "vfio" > /etc/cra/uplink-mode
}

main() {
    need_root
    local cmd="${1:-}"; shift || true
    # Parse common flags.
    local positional=()
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --size)  HUGE_SIZE="$2"; shift 2 ;;
            --count) HUGE_COUNT="$2"; shift 2 ;;
            *)       positional+=("$1"); shift ;;
        esac
    done
    set -- "${positional[@]:-}"

    case "$cmd" in
        hugepages) prep_hugepages ;;
        iommu)     prep_iommu ;;
        bind)      prep_bind "$@" ;;
        isolate)   prep_isolate "${1:-}" ;;
        all)       prep_hugepages; [[ $# -ge 1 ]] && prep_bind "$@" ;;
        *)
            echo "usage: $0 {hugepages|iommu|bind|isolate|all} [opts]" >&2
            echo "  hugepages [--size 2M|1G] [--count N]" >&2
            echo "  iommu                          # edits GRUB (reboot needed)" >&2
            echo "  bind <pci-addr>...             # bind uplinks to vfio-pci" >&2
            echo "  isolate <cpu-list>             # edits GRUB (reboot needed)" >&2
            echo "  all [--count N] <pci-addr>...  # hugepages + bind" >&2
            exit 2
            ;;
    esac

    echo
    echo ">> After hugepages + IOMMU (rebooted) + vfio-pci binding, run grout"
    echo "   WITHOUT -t (edit /etc/default/grout: ARGS=\"\") for the production"
    echo "   fast path. The lab/MVP path keeps ARGS=\"-t\" (net_tap uplinks)."
}

main "$@"
