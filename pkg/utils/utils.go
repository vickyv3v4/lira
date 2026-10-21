package utils

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"gitlab.msxf.com/mscloud/lira/pkg/types"

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

func GeneratePodNodeENIName(ecsId string) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 4)
	for i := range b {
		b[i] = letters[r.Intn(len(letters))]
	}
	str := ecsId + string(b)
	return fmt.Sprintf("mck-%s-eni", str)
}

func GenerateENIGrUniqueID(ecsId string) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = letters[r.Intn(len(letters))]
	}
	str := ecsId + string(b)
	return fmt.Sprintf("mck-%s", str)
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

func DiffStringSlice(a, b []string) []string {
	var diff []string
	if len(a) <= len(b) {
		setA := make(map[string]struct{}, len(a))
		for _, s := range a {
			setA[s] = struct{}{}
		}
		for _, s := range b {
			if _, exists := setA[s]; !exists {
				diff = append(diff, s)
			}
		}

	} else if len(a) >= len(b) {
		setB := make(map[string]struct{}, len(b))
		for _, s := range b {
			setB[s] = struct{}{}
		}
		for _, s := range a {
			if _, exists := setB[s]; !exists {
				diff = append(diff, s)
			}
		}
	}
	return diff
}
