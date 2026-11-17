---
title: SBOM Attestations
description: >-
  Verify the signed SPDX SBOM attestation published with each release image.
---

# SBOM Attestations

Release builds (`draft_release.yaml` on `v*` tags) generate an SPDX SBOM for
every image and publish it as a keyless, signed in-toto attestation with
`cosign attest --type spdxjson`. The SBOM is also uploaded as a workflow
artifact (`sbom-<image>`).

The attestation is signed by the reusable workflow
`.github/workflows/build-images.yaml` at the release tag, the same certificate
identity as the image signature. The workflow signs with cosign v3, which
stores the attestation as a Sigstore bundle (OCI referrer); cosign v2 looks
for the legacy format by default and does not find it. Verify it with cosign
v3 or later:

```bash
cosign verify-attestation \
  --type spdxjson \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/telekom/das-schiff-network-operator/\.github/workflows/build-images\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' \
  ghcr.io/telekom/das-schiff-network-operator:<version>
```

`<version>` is the release without the leading `v`. The identity pattern also
matches pre-release tags such as `v0.5.1-alpha.1`. Replace the image name to
check any other image of the release.

Limitations:

- The attestation is attached to the multi-arch manifest list (the digest a tag
  resolves to), not to the per-architecture manifests. Verify the tag or the
  manifest-list digest, not a platform-specific digest.
- The SBOM is generated for the `linux/amd64` image of the manifest list.
- Releases published before this change carry an unsigned SBOM attached with
  `cosign attach sbom` instead and have no attestation to verify.
