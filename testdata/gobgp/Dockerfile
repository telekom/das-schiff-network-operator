FROM ubuntu:22.04

RUN apt-get update && apt-get install -y iproute2 tar python3 python3-pip

RUN pip3 install grpcio protobuf

ADD https://github.com/osrg/gobgp/releases/download/v3.13.0/gobgp_3.13.0_linux_amd64.tar.gz /tmp/gobgp.tar.gz
RUN tar xfvz /tmp/gobgp.tar.gz -C /tmp/ && mv /tmp/gobgp /usr/bin/ && mv /tmp/gobgpd /usr/bin/

RUN mkdir -p /tool/
ADD /tool/* /tool/

ADD config.toml /config.toml

ENTRYPOINT [ "/usr/bin/gobgpd", "--config-file", "/config.toml" ]
