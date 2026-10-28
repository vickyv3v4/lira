//go:build linux
// +build linux

package ebpf

import (
	"testing"
	"unsafe"
)

// 与 bpf/lira.bpf.c 中结构体逐一对应的大小断言（设计 §6：
// Go 控制面与 C 数据面共享 map 布局，两边 sizeof 不一致会直接写坏 map 条目）。
func TestBPFLayoutSizes(t *testing.T) {
	cases := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"PodIPValue", unsafe.Sizeof(PodIPValue{}), 4},
		{"PairKey", unsafe.Sizeof(PairKey{}), 12},
		{"CIDRKey", unsafe.Sizeof(CIDRKey{}), 16},
		{"CIDRValue", unsafe.Sizeof(CIDRValue{}), 2 + 2*MaxPortsPerCIDR},
		{"ISOKey", unsafe.Sizeof(ISOKey{}), 8},
		{"SlaveSrcValue", unsafe.Sizeof(SlaveSrcValue{}), 4},
		{"NodeValue", unsafe.Sizeof(NodeValue{}), 4},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s size = %d, want %d (C side mismatch)", c.name, c.got, c.want)
		}
	}
}
