#!/bin/bash
tag=$1
if [ -z "$1" ]; then
  echo "please input tag: $0 v0.1"
  exit 1
fi
TAG=$tag make init-img;docker tag docker.io/library/lira-init-cni:$tag www.harbor-test1002.com/mscloud-mck/lira-init-cni:$tag;docker push www.harbor-test1002.com/mscloud-mck/lira-init-cni:$tag

TAG=$tag make daemon-img;docker tag docker.io/library/lira-daemon:$tag www.harbor-test1002.com/mscloud-mck/lira-daemon:$tag;docker push www.harbor-test1002.com/mscloud-mck/lira-daemon:$tag

TAG=$tag make controller-img;docker tag docker.io/library/lira-controller:$tag www.harbor-test1002.com/mscloud-mck/lira-controller:$tag;docker push www.harbor-test1002.com/mscloud-mck/lira-controller:$tag

TAG=$tag make resource-img;docker tag docker.io/library/lira-resource:$tag www.harbor-test1002.com/mscloud-mck/lira-resource:$tag; docker push www.harbor-test1002.com/mscloud-mck/lira-resource:$tag
