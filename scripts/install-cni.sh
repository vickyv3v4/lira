#!/bin/sh

set -o nounset

mkdir -p /opt/cni/bin/

\cp -f /lira /opt/cni/bin/
chmod +x /opt/cni/bin/lira

\cp -f /01-lira.conflist /etc/cni/net.d/01-lira.conflist

# eBPF 数据面对象（网络策略功能启用时存在；init 容器安装到宿主机固定路径）
mkdir -p /opt/cni/lira/
if [ -f /lira.bpf.o ]; then
  \cp -f /lira.bpf.o /opt/cni/lira/lira.bpf.o
fi

masq_eni_only() {
  echo "exec iptables configure lira-masq"
  if ! "$1" -t nat -L lira-masq; then
    # Create a new chain in nat table.
    "$1" -t nat -N lira-masq
  fi

  if ! "$1" -t nat -L POSTROUTING | grep -q lira-masq; then
    # Append that chain to POSTROUTING table.
    "$1" -t nat -A POSTROUTING -m comment --comment "lira:masq-outgoing" ! -o lo -j lira-masq
  fi

  if ! "$1" -t nat -L lira-masq | grep -q MASQUERADE; then
    "$1" -t nat -A lira-masq -j MASQUERADE
  fi
}

masq_eni_only iptables