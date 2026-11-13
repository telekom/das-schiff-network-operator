---
title: CRA-VSR Agent Configuration
description: >-
  Environment variables of the CRA-VSR agent and the SSH settings of its
  NETCONF client.
---

# CRA-VSR Agent Configuration

The `agent-cra-vsr` DaemonSet configures the 6WIND Virtual Service Router (VSR)
on its node over NETCONF on SSH. It reads these environment variables at start:

| Variable | Required | Meaning |
|----------|----------|---------|
| `CRA_URL` | yes | Comma-separated `host:port` list of the router's NETCONF endpoints, e.g. `169.254.33.1:830`. |
| `CRA_TIMEOUT` | yes | Timeout per NETCONF operation as a Go duration, e.g. `30s`. |
| `CRA_USER` | yes | NETCONF user name. |
| `CRA_PASSWORD` | yes | NETCONF password. |
| `CRA_KNOWN_HOSTS` | yes | Path of an OpenSSH `known_hosts` file with the router's host key. |
| `CRA_SSH_MIN_RSA_HOST_KEY_BITS` | no | Smallest accepted RSA host key, default `3072`. **Opt-out, not PSA-compliant below 3072.** |
| `CRA_SSH_LEGACY_ALGORITHMS` | no | `true` uses the `golang.org/x/crypto/ssh` default algorithms instead of the lists below. **Opt-out, not PSA-compliant.** |

## Host key verification

The agent refuses to start when `CRA_KNOWN_HOSTS` is unset, when the file cannot
be read, or when it has no entry for one of the `CRA_URL` endpoints. Entries for
a port other than 22 use the `[host]:port` form:

```text
[169.254.33.1]:830 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA...
```

Collect the key on the node with `ssh-keyscan -p 830 -t ed25519 169.254.33.1`
and compare its fingerprint with the router's console before trusting it.

At every connect the client closes the connection before authentication when the
router presents a host key that does not match the file, or an RSA host key
shorter than 3072 bits.

## SSH algorithms

The NETCONF client negotiates only these algorithms, in order of preference:

| Category | Algorithms |
|----------|------------|
| Key exchange | `curve25519-sha256`, `ecdh-sha2-nistp521`, `ecdh-sha2-nistp384`, `ecdh-sha2-nistp256`, `diffie-hellman-group16-sha512` |
| MAC | `hmac-sha2-512-etm@openssh.com`, `hmac-sha2-256-etm@openssh.com`, `hmac-sha2-512`, `hmac-sha2-256` |
| Host key | `ssh-ed25519`, `ecdsa-sha2-nistp521`, `ecdsa-sha2-nistp384`, `ecdsa-sha2-nistp256`, `rsa-sha2-512`, `rsa-sha2-256` |

For each `CRA_URL` the client requests only the host key algorithms that match
the key types `known_hosts` holds for that endpoint (an `ssh-rsa` entry maps to
`rsa-sha2-512` and `rsa-sha2-256`). A router that also has an Ed25519 key
therefore still presents the pinned RSA key.

Ciphers are the `golang.org/x/crypto/ssh` defaults (AES-GCM, ChaCha20-Poly1305,
AES-CTR). A router that offers none of the algorithms of one category fails the
handshake; the agent logs the negotiation error and does not send credentials.

## Opt-outs (not PSA-compliant)

Use these only for a router that cannot meet the settings above, and only until
the router is fixed. The agent logs a warning at start when one is active.

- `CRA_SSH_MIN_RSA_HOST_KEY_BITS=2048` accepts shorter RSA host keys.
- `CRA_SSH_LEGACY_ALGORITHMS=true` negotiates the `golang.org/x/crypto/ssh`
  default key exchanges, MACs and host key algorithms, which include
  `diffie-hellman-group14-sha1`.

Host key verification against `CRA_KNOWN_HOSTS` has no opt-out.
