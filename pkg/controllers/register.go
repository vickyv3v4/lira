package controllers

import (
	"context"

	"gitlab.msxf.com/mscloud/lira/pkg/msxf"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type ControllerCtx struct {
	context.Context
	MsxfClient       msxf.MsxfClientAPI
	RegisterResource []client.Object
	MsConfig         *MSConfig
	PodENICurrent    int
}

type Creator func(mgr manager.Manager, ctrlCtx *ControllerCtx) error

// Controllers collect for all controller
var Controllers = map[string]struct {
	Creator Creator
	Enable  bool
}{}

// Add add controller by name
func Add(name string, creator Creator, enable bool) {
	Controllers[name] = struct {
		Creator Creator
		Enable  bool
	}{
		Creator: creator,
		Enable:  enable,
	}
}

var Resources = map[string]struct {
	Creator Creator
	Enable  bool
}{}

func AddRes(name string, creator Creator, enable bool) {
	Resources[name] = struct {
		Creator Creator
		Enable  bool
	}{
		Creator: creator,
		Enable:  enable,
	}
}
