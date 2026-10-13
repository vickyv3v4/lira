# Lira

MCK-CNI，支持pod直通vpc网络

## Build

### build all images
```
bash build_dev_img.sh v0.01
```

### build init-img
```
make init-img

docker tag docker.io/library/lira-init-cni:v0.1 www.harbor-test1002.com/mscloud-mck/lira-init-cni:v0.1

docker push www.harbor-test1002.com/mscloud-mck/lira-init-cni:v0.1
```
### build daemon-img
example: 
```
make daemon-img

docker tag docker.io/library/lira-daemon:v0.1 www.harbor-test1002.com/mscloud-mck/lira-daemon:v0.1

docker push www.harbor-test1002.com/mscloud-mck/lira-daemon:v0.1
```
### build controller-img
```
make controller-img

docker tag docker.io/library/lira-controller:v0.1 www.harbor-test1002.com/mscloud-mck/lira-controller:v0.1

docker push www.harbor-test1002.com/mscloud-mck/lira-controller:v0.1
```

### build rpc
```
cd pkg/rpc

protoc --go_out=. --go-grpc_out=. rpc.proto 
```