package policyagent

import (
	"hash/fnv"
	"sort"
	"strings"
)

// 身份模型（设计 §3）：identityID = fnv1a32("ns/k1=v1,k2=v2") & 0x7FFFFFFF。
// 输入规范化（键排序）保证同一命名空间同标签的 pod 在所有节点得到一致身份，
// 数据面无需依赖任何中心化身份分配服务。
// 0/1 保留（unknown/host），计算结果命中保留值时偏移 +2。

// IdentityFor 由命名空间与 pod 标签计算稳定身份 ID
func IdentityFor(namespace string, labels map[string]string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(identityString(namespace, labels)))
	id := h.Sum32() & 0x7FFFFFFF
	if id < 2 {
		id += 2
	}
	return id
}

// identityString 规范化身份输入：ns + 按键排序的标签串
func identityString(namespace string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(namespace)
	for _, k := range keys {
		b.WriteByte('/')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}
