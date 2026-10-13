package types

import (
	"gitlab.msxf.com/mscloud/lira/pkg/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

var Scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(corev1.AddToScheme(Scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(Scheme))
	utilruntime.Must(v1beta1.AddToScheme(Scheme))
}
