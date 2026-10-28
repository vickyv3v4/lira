//go:build !linux
// +build !linux

package ebpf

import (
	"context"
	"errors"
	"net"
)

var ErrUnsupported = errors.New("ebpf network policy is only supported on linux")

// 非 linux 平台桩：保持交叉编译可过，运行期不可用。
type Manager struct{}

func NewManager(objectPath string) (*Manager, error) { return nil, ErrUnsupported }

func (m *Manager) UpsertPodIP(ip net.IP, identity uint32) error    { return ErrUnsupported }
func (m *Manager) DeletePodIP(ip net.IP) error                     { return ErrUnsupported }
func (m *Manager) WriteNodeConfig(nodeIP net.IP) error             { return ErrUnsupported }
func (m *Manager) EnsureENIAttached(eniMACs []string) error        { return ErrUnsupported }
func (m *Manager) DrainEvents(ctx context.Context)                 {}
func (m *Manager) ReplaceIsolated(entries []ISOKey) error          { return ErrUnsupported }
func (m *Manager) ReplacePair(keys []PairKey) error                { return ErrUnsupported }
func (m *Manager) ReplaceCIDR(entries map[CIDRKey]CIDRValue) error { return ErrUnsupported }
