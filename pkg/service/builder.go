package service

import (
	"context"
	"fmt"
	"os"

	"gitlab.msxf.com/mscloud/lira/pkg/k8s"
)

type RPCServiceBuilder struct {
	ctx context.Context
	//configFilePath string
	namespace string
	service   *rpcService
	err       error
}

func NewRPCServiceBuilder(ctx context.Context) *RPCServiceBuilder {
	return &RPCServiceBuilder{ctx: ctx}
}
func (r *RPCServiceBuilder) InitConfig() *RPCServiceBuilder {
	if r.err != nil {
		return r
	}
	r.namespace = os.Getenv("POD_NAMESPACE")
	return r
}
func (r *RPCServiceBuilder) InitConfigFilePath(configFilePath string) *RPCServiceBuilder {
	if r.err != nil {
		return r
	}
	//r.configFilePath = configFilePath
	return r
}

func (r *RPCServiceBuilder) InitService() *RPCServiceBuilder {
	if r.err != nil {
		return r
	}
	r.service = &rpcService{}
	return r
}
func (r *RPCServiceBuilder) InitK8s() *RPCServiceBuilder {
	if r.err != nil {
		return r
	}
	var err error
	if r.service.k8s, err = k8s.NewK8S(r.namespace); err != nil {
		r.err = fmt.Errorf("error init k8s: %w", err)
		return r
	}
	return r
}

func (r *RPCServiceBuilder) Build() (*rpcService, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.service, nil
}

func NewService(ctx context.Context) (*rpcService, error) {
	builder := NewRPCServiceBuilder(ctx).
		InitConfig().
		InitService().
		InitK8s()
	return builder.Build()
}
