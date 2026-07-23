#!/bin/bash
# Stop the CRA container (grout flavour). Mirrors the vSR overlay cra-stop.sh.
set -euo pipefail

CRA_CIDFILE="/run/cra/.dockerid"
NS="hbr"

[[ -f "$CRA_CIDFILE" ]] || { echo "No CRA container ID file"; exit 0; }
CONTAINER_ID=$(cat "$CRA_CIDFILE")

if nerdctl --namespace="$NS" ps -q --no-trunc | grep -q "^${CONTAINER_ID}$"; then
    nerdctl --namespace="$NS" stop "$CONTAINER_ID" || true
    nerdctl --namespace="$NS" rm "$CONTAINER_ID" || true
    echo "CRA grout container stopped"
else
    echo "CRA grout container not running"
fi
rm -f "$CRA_CIDFILE"
exit 0
