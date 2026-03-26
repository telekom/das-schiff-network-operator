#!/bin/bash
# Load CRA image and set flavour.
# Adapted from ansible hbn_node_deps role for e2e use.
#
# Usage:
#   load-cra-image.sh frr <source-image>
#
# Examples:
#   load-cra-image.sh frr docker-archive:/tmp/cra-frr.tar:das-schiff-cra-frr:latest
#   load-cra-image.sh frr docker://registry.example.com/cra-frr:v1.0

set -euo pipefail

usage() {
    cat <<EOF
Usage: $0 frr <source-image>

Arguments:
  flavour       CRA flavour (only 'frr' supported in e2e)
  source-image  Full skopeo source specification

Examples:
  $0 frr docker-archive:/tmp/cra-frr.tar:das-schiff-cra-frr:latest
  $0 frr docker://registry.example.com/cra-frr:v1.0
EOF
    exit 1
}

if [[ $# -lt 2 ]]; then
    usage
fi

FLAVOUR="$1"
SOURCE_IMAGE="$2"

if [[ "$FLAVOUR" != "frr" ]]; then
    echo "ERROR: Only 'frr' flavour is supported in e2e" >&2
    exit 1
fi

# Ensure /etc/cra directory exists
mkdir -p /etc/cra

# Write flavour to file
echo "$FLAVOUR" > /etc/cra/flavour
echo "Set CRA flavour to: $FLAVOUR"

echo "Loading FRR image with umoci..."

# Extract version from source image (everything after last :)
VERSION="${SOURCE_IMAGE##*:}"

# Download with skopeo to OCI format
skopeo copy "$SOURCE_IMAGE" "oci:/tmp/cra-frr:${VERSION}"

# Extract with umoci
umoci unpack --image "/tmp/cra-frr:${VERSION}" /var/lib/machines/cra-frr

# Clean up
rm -rf /tmp/cra-frr

echo "FRR image extracted to /var/lib/machines/cra-frr"
echo "CRA image loaded successfully"
