---
title: Installation
description: >-
  Install the network-operator CRDs and controller into a Kubernetes cluster.
---

# Installation

This page covers installing the operator so you can start authoring intent
resources. It assumes you have a Kubernetes cluster and `kubectl` configured for
it.

!!! note "Prerequisites"
    - A Kubernetes cluster with nodes running the network-operator agents /
      Containerized Routing Agent (CRA). In an HBN deployment this includes the
      CRA (FRR or 6WIND vSR) on each node.
    - `kubectl` access with cluster-admin (installing CRDs and RBAC).
    - For building from source: Go (see `go.mod`), `make`, and the toolchain
      dependencies listed in the repository `README`.
    - The foundation resources (`VRF`, `Network`) are typically created by an
      infrastructure provisioner. For a self-contained trial you can create them
      by hand — see the [Quick Start](quick-start.md).

## Container images

Official images are published to `ghcr.io/telekom`. The operator image is
`ghcr.io/telekom/das-schiff-network-operator`.

### Verify image signatures

Every image is signed keyless with [cosign](https://docs.sigstore.dev/) by the
reusable workflow `.github/workflows/build-images.yaml`. Release builds run it
from `draft_release.yaml` on `v*` tags; builds of `main` run it from
`container-image.yaml` and are signed too, but with a different certificate
identity. The certificate identity is the reusable workflow at the ref that
triggered the build, so a release image carries
`https://github.com/telekom/das-schiff-network-operator/.github/workflows/build-images.yaml@refs/tags/vX.Y.Z`
and a `main` image carries `...@refs/heads/main`.

Verify a release image against the tag-anchored identity (cosign v2 or later):

```bash
failed=0
IDENTITY='^https://github\.com/telekom/das-schiff-network-operator/\.github/workflows/build-images\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'

for image in \
  das-schiff-network-operator \
  das-schiff-cra-frr \
  das-schiff-nwop-agent-cra-frr \
  das-schiff-nwop-agent-cra-vsr \
  das-schiff-nwop-agent-netplan \
  das-schiff-nwop-agent-hbn-l2 \
  das-schiff-network-sync \
  das-schiff-platform-coil \
  das-schiff-platform-metallb; do
  cosign verify \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp "$IDENTITY" \
    "ghcr.io/telekom/${image}:<version>" > /dev/null \
    && echo "OK ${image}" || { echo "FAILED ${image}"; failed=1; }
done
[ "$failed" -eq 0 ]  # non-zero status if any image failed
```

`<version>` is the release without the leading `v` (for example `0.5.8`).
A `main` build fails this check with
`expected SAN value to match regex ..., got ...@refs/heads/main`.
The regexp only accepts final releases; to also accept pre-releases such as
`v0.5.1-alpha.8`, append `(-[0-9A-Za-z.-]+)?` before the `$`.
Release images are signed recursively, so each per-architecture manifest
verifies on its own as well. Tags are mutable: deploy by digest and verify
that digest (`ghcr.io/telekom/<image>@sha256:...`).

Release builds also attach an SPDX SBOM to each image (and upload it as a
workflow artifact). The SBOM is attached with `cosign attach sbom` and is
**not signed**; no signed attestations (SBOM or provenance) are published, so
there is nothing to check with `cosign verify-attestation`. Treat the SBOM as
informational.

Example (not tested in CI) that enforces the same identity at admission time with the
[Sigstore policy-controller](https://docs.sigstore.dev/policy-controller/overview/):

```yaml
apiVersion: policy.sigstore.dev/v1beta1
kind: ClusterImagePolicy
metadata:
  name: das-schiff-network-operator-releases
spec:
  images:
    - glob: "ghcr.io/telekom/das-schiff-network-operator*"
    - glob: "ghcr.io/telekom/das-schiff-network-sync*"
    - glob: "ghcr.io/telekom/das-schiff-cra-frr*"
    - glob: "ghcr.io/telekom/das-schiff-nwop-agent-*"
    - glob: "ghcr.io/telekom/das-schiff-platform-*"
  authorities:
    - keyless:
        url: https://fulcio.sigstore.dev
        identities:
          - issuer: https://token.actions.githubusercontent.com
            subjectRegExp: '^https://github\.com/telekom/das-schiff-network-operator/\.github/workflows/build-images\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
      ctlog:
        url: https://rekor.sigstore.dev
```

The policy-controller only enforces policies in namespaces labelled
`policy.sigstore.dev/include=true`, so label the namespace the operator runs
in (by default `kube-system`).

## Install CRDs

The custom resource definitions are built with Kustomize and applied with:

```bash
make install
```

This runs `kustomize build config/crd | kubectl apply -f -`. To remove them
again:

```bash
make uninstall
```

## Deploy the operator

Deploy the controller (and, where applicable, agents) with:

```bash
# Optionally override the image; defaults to ghcr.io/telekom/...:latest
make deploy IMG=ghcr.io/telekom/das-schiff-network-operator:<tag>
```

This runs `kustomize build config/default | kubectl apply -f -`. To remove it:

```bash
make undeploy
```

!!! tip "Webhooks and cert-manager"
    The intent API group ships validating/defaulting webhooks. If your cluster
    uses cert-manager for webhook certificates, install them with
    `make install-certs` (and `make uninstall-certs` to remove). Review
    `config/` for the exact overlays your environment needs.

## Verify the installation

```bash
# CRDs registered
kubectl get crds | grep network-connector.sylvaproject.org

# Operator pod(s) running
kubectl get pods -A | grep network-operator
```

You should see the `network-connector.sylvaproject.org` CRDs (VRF, Network,
Destination, Layer2Attachment, Inbound, Outbound, PodNetwork, BGPPeering,
Collector, TrafficMirror, …) and a running operator deployment.

## Node readiness

During initialization nodes may be tainted until the network stack is ready. The
operator removes configured taints and sets a `NetworkOperatorReady` node
condition once health checks pass. See [Node Readiness](../reference/node-readiness.md)
for configuration and troubleshooting.

## Next steps

- [Concepts](concepts.md) — understand the model before you author resources.
- [Quick Start](quick-start.md) — a minimal end-to-end example.
