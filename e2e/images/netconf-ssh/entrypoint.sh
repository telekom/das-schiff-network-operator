#!/bin/sh
# Starts three sshd instances that differ only in what the client must reject:
#   830  default OpenSSH algorithms, ed25519 host key   -> client connects
#   831  only diffie-hellman-group14-sha1               -> handshake fails
#   832  2048-bit RSA host key                          -> host key rejected
set -eu

ssh-keygen -q -t ed25519 -N '' -f /etc/ssh/host_ed25519
ssh-keygen -q -t rsa -b 2048 -N '' -f /etc/ssh/host_rsa2048

echo "netconf:${NETCONF_PASSWORD:?}" | chpasswd
install -d -o netconf -m 0700 /home/netconf/.ssh
printf '%s\n' "${NETCONF_AUTHORIZED_KEY:-}" > /home/netconf/.ssh/authorized_keys
chown netconf /home/netconf/.ssh/authorized_keys
chmod 0600 /home/netconf/.ssh/authorized_keys

/usr/sbin/sshd -e -f /etc/ssh/sshd_config.weak-kex
/usr/sbin/sshd -e -f /etc/ssh/sshd_config.rsa2048
exec /usr/sbin/sshd -D -e -f /etc/ssh/sshd_config.default
