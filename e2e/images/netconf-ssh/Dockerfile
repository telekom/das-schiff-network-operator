# OpenSSH NETCONF endpoint for the CRA-VSR NETCONF client E2E test
# (e2etests/netconf). The netconf subsystem only answers the hello exchange.
FROM alpine:3.22

RUN apk add --no-cache openssh-server && \
    adduser -D netconf

COPY sshd_config.common sshd_config.default sshd_config.weak-kex sshd_config.rsa2048 /etc/ssh/
COPY netconf-hello entrypoint.sh /usr/local/bin/

EXPOSE 830 831 832
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
