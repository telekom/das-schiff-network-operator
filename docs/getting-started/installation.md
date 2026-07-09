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
