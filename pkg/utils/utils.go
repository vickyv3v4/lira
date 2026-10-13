package utils

import (
	"encoding/json"
	"fmt"
	"gitlab.msxf.com/mscloud/lira/pkg/types"
	"strconv"
	"strings"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
)

func PodInfoKey(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// JSONStr json to str
func JSONStr(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// PodSandboxExited pod or sandbox is exited
func PodSandboxExited(p *corev1.Pod) bool {
	switch p.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		return true
	default:
		return false
	}
}
func GenerateENIName(ecsId string) string {
	return fmt.Sprintf("mck-%s-eni", ecsId)
}

func IsValidNumber(s string) (bool, int) {
	// 移除字符串中的逗号
	s = strings.Replace(s, ",", "", -1)
	// 尝试将字符串转换为整数
	v, err := strconv.Atoi(s)
	return err == nil, v
}

func GenerateRequestId() string {
	uuid, err := uuid.NewRandom()
	if err != nil {
		fmt.Println("Error generating requestId UUID:", err)
		return ""
	}
	return uuid.String()
}

func IsMasterNode(node *corev1.Node) bool {
	labels := node.Labels
	_, ec := labels[types.LabelRoleControl]
	_, em := labels[types.LabelRoleMaster]
	if ec || em {
		return true
	}
	return false
}
