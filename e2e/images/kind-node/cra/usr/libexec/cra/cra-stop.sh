#!/bin/bash
# Stop the CRA container (FRR: managed by systemd-nspawn, nothing to do here)
set -euo pipefail
echo "CRA stop: nspawn managed by systemd"
exit 0
