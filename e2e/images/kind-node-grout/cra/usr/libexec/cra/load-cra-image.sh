#!/bin/bash
# Load the grout CRA image into containerd namespace hbr at node boot.
#
# grout overlay. Unlike the base FRR e2e (systemd-nspawn machine baked at build
# time), grout runs as a nerdctl container in namespace "hbr" (like vSR), so its
# image is imported into containerd. grout is open-source, so this can also run
# at build time; it is kept as a script for parity with the vSR overlay.
#
# Usage: load-cra-image.sh <source-image> <image-name:tag>
#   e.g. load-cra-image.sh \
#     docker-archive:/tmp/cra-grout.tar:das-schiff-cra-grout:latest \
#     das-schiff-cra-grout:latest
#   or:  load-cra-image.sh docker://quay.io/grout/grout:edge grout:edge
set -euo pipefail

SOURCE_IMAGE="${1:?source image required}"
IMAGE_NAME_TAG="${2:?image-name:tag required}"

mkdir -p /etc/cra
echo "grout" > /etc/cra/flavour

SAFE_NAME=$(echo "$IMAGE_NAME_TAG" | tr ':/' '-')
TMP_TAR="/tmp/cra-grout-${SAFE_NAME}.tar"
skopeo copy "$SOURCE_IMAGE" "docker-archive:${TMP_TAR}:${IMAGE_NAME_TAG}"
ctr -n=hbr images import "$TMP_TAR"
rm -f "$TMP_TAR"
echo "$IMAGE_NAME_TAG" > /etc/cra/image
echo "grout image loaded into hbr: $IMAGE_NAME_TAG"
