#!/bin/bash
tag=$1
if [ -z "$1" ]; then
  echo "please input tag: $0 v0.1"
  exit 1
fi
INIT_IMG=lira-init-cni:$tag make init-img;docker tag docker.io/library/lira-init-cni:$tag www.harbor-test1002.com/mscloud-mck/lira-init-cni:$tag;docker push www.harbor-test1002.com/mscloud-mck/lira-init-cni:$tag

DAEMON_IMG=lira-daemon:$tag make daemon-img;docker tag docker.io/library/lira-daemon:$tag www.harbor-test1002.com/mscloud-mck/lira-daemon:$tag;docker push www.harbor-test1002.com/mscloud-mck/lira-daemon:$tag

CONTROLLER_IMG=lira-controller:$tag make controller-img;docker tag docker.io/library/lira-controller:$tag www.harbor-test1002.com/mscloud-mck/lira-controller:$tag;docker push www.harbor-test1002.com/mscloud-mck/lira-controller:$tag

RESOURCE_IMG=lira-resource:$tag make resource-img;docker tag docker.io/library/lira-resource:$tag www.harbor-test1002.com/mscloud-mck/lira-resource:$tag; docker push www.harbor-test1002.com/mscloud-mck/lira-resource:$tag