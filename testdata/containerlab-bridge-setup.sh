# 2 Servers
for i in $(seq 4 5)
do
  # with 2 ports each
  for j in $(seq 1 2)
  do
    ip link add srv$i-p$j type bridge
    ip link set dev srv$i-p$j mtu 9100
    ip link set dev srv$i-p$j up
    echo 1 > /proc/sys/net/ipv6/conf/srv$i-p$j/disable_ipv6
  done
done