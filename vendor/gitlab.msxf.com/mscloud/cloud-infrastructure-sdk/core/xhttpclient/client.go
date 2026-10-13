package xhttpclient

import (
	"context"
	"gitlab.msxf.com/icloud/cloud-common-go/core/xhttp"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/core/xcontext"
)

var client = xhttp.NewHttpClient(xhttp.NewDefaultConfig())

func Get(ctx context.Context, request *xhttp.Request) *xhttp.Response {
	return client.Get(wrapHeaders(ctx, request))
}

func Post(ctx context.Context, request *xhttp.Request) *xhttp.Response {
	return client.Post(wrapHeaders(ctx, request))
}

func Put(ctx context.Context, request *xhttp.Request) *xhttp.Response {
	return client.Put(wrapHeaders(ctx, request))
}

func Delete(ctx context.Context, request *xhttp.Request) *xhttp.Response {
	return client.Delete(wrapHeaders(ctx, request))
}

func wrapHeaders(ctx context.Context, request *xhttp.Request) *xhttp.Request {
	t, _ := ctx.Value(xcontext.PropagationType).(string)
	c, _ := ctx.Value(xcontext.PropagationKey).(string)
	if request.HttpHeaders == nil {
		headers := make(xhttp.HttpHeaders)
		headers[xcontext.PropagationType] = t
		headers[xcontext.PropagationKey] = c
		request.HttpHeaders = headers
		return request
	}
	request.HttpHeaders[xcontext.PropagationKey] = c
	request.HttpHeaders[xcontext.PropagationType] = t
	return request
}
