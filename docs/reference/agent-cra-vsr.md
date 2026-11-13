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
| `CRA_PRIVATE_KEY_FILE` | one of the two | Path of an unencrypted OpenSSH or PEM private key. The client authenticates with this key only. |
| `CRA_PASSWORD_FILE` | one of the two | Path of a file that holds the NETCONF password (trailing newlines are removed). Used only when `CRA_PRIVATE_KEY_FILE` is unset. |
| `CRA_KNOWN_HOSTS` | yes | Path of an OpenSSH `known_hosts` file with the router's host key. |

## Credentials

The agent refuses to start when neither `CRA_PRIVATE_KEY_FILE` nor
`CRA_PASSWORD_FILE` is set, when the file is missing, cannot be parsed, or has a
mode wider than `0600` (any group or other permission, or the owner execute bit).
Neither the key nor the password appears in a log line or error message.

The default manifest (`config/agent-cra-vsr/agent.yaml`) mounts the host
directory `/etc/cra/netconf` read-only at the same path and sets
`CRA_PRIVATE_KEY_FILE=/etc/cra/netconf/id_ed25519`. The node creates that key
pair at first boot and enrols the public key on the router.

### Migrating from `CRA_PASSWORD`

`CRA_PASSWORD` is no longer read. An agent that still has only `CRA_PASSWORD`
set exits at start with an error that names `CRA_PASSWORD_FILE`. Choose one:

- **Key authentication (default):** provide `/etc/cra/netconf/id_ed25519` on
  the node and enrol its public key for the NETCONF user on the router.
- **Password authentication (opt-out for routers without key support):** write
  the password to a file with mode `0600` (or `0400`), e.g.
  `/etc/cra/netconf/password`, and replace `CRA_PRIVATE_KEY_FILE` by
  `CRA_PASSWORD_FILE` in the DaemonSet:

  ```yaml
  - op: replace
    path: /spec/template/spec/containers/0/env/3
    value:
      name: CRA_PASSWORD_FILE
      value: /etc/cra/netconf/password
  ```

  When mounting the password from a Kubernetes Secret instead, set
  `defaultMode: 0400` on the volume; the Secret default `0644` is refused.

Remove `CRA_PASSWORD` from the manifest. While a file variable is set, a left-over
`CRA_PASSWORD` is ignored and logged.

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

Ciphers are the `golang.org/x/crypto/ssh` defaults (AES-GCM, ChaCha20-Poly1305,
AES-CTR). A router that offers none of the algorithms of one category fails the
handshake; the agent logs the negotiation error and does not send credentials.
