//go:build linux
// +build linux

package ebpf

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
)

// Manager 数据面对象持有者：负责加载/复用 pin 目录中的 map 与 program，
// 挂载宿主机侧执行点（①②），并提供控制面写 map 的入口（设计 §5.1/§5.2）。
type Manager struct {
	maps  map[string]*ebpf.Map
	progs map[string]*ebpf.Program
}

// NewManager 加载 lira.bpf.o 并 pin 到 PinRoot。
// 若 pin 目录已存在（daemon 重启），直接复用 pinned 对象，程序不重新加载。
func NewManager(objectPath string) (*Manager, error) {
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("error remove memlock rlimit, %w", err)
	}

	m := &Manager{
		maps:  make(map[string]*ebpf.Map),
		progs: make(map[string]*ebpf.Program),
	}

	if err := os.MkdirAll(pinDir("progs"), 0700); err != nil {
		return nil, fmt.Errorf("error create pin dir, %w", err)
	}
	if err := os.MkdirAll(pinDir("maps"), 0700); err != nil {
		return nil, fmt.Errorf("error create pin dir, %w", err)
	}

	// map：优先复用 pinned（daemon 重启场景，保证 agent 已写入的策略不丢）
	if err := m.loadMaps(); err != nil {
		return nil, err
	}
	// program：pinned 存在则复用；否则从 ELF 加载并 pin
	if err := m.loadPrograms(objectPath); err != nil {
		return nil, err
	}
	return m, nil
}

func pinDir(kind string) string {
	return filepath.Join(PinRoot, kind)
}

func (m *Manager) loadMaps() error {
	for _, name := range []string{MapSlaveSrc, MapPodIP, MapPolicyID, MapPolicyCIDR, MapIsolated, MapCT, MapNodeCfg, MapEvents} {
		p := filepath.Join(pinDir("maps"), name)
		mp, err := ebpf.LoadPinnedMap(p, nil)
		if err == nil {
			m.maps[name] = mp
			continue
		}
		m.maps[name] = nil // 标记缺失，等待 loadPrograms 从 ELF 统一创建
	}
	return nil
}

func (m *Manager) loadPrograms(objectPath string) error {
	missingProg := false
	for _, name := range []string{ProgMainEgress, ProgMainIngress, ProgVethEgress, ProgPodEgress, ProgPodIngress} {
		p := filepath.Join(pinDir("progs"), name)
		prog, err := ebpf.LoadPinnedProgram(p, nil)
		if err == nil {
			m.progs[name] = prog
			continue
		}
		missingProg = true
		m.progs[name] = nil
	}

	// 全部对象均已 pin：无需加载 ELF
	if !missingProg && m.maps[MapPodIP] != nil {
		return nil
	}
	if _, err := os.Stat(objectPath); err != nil {
		return fmt.Errorf("bpf object %s not available: %w", objectPath, err)
	}

	spec, err := ebpf.LoadCollectionSpec(objectPath)
	if err != nil {
		return fmt.Errorf("error load bpf collection spec, %w", err)
	}
	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return fmt.Errorf("error create bpf collection (kernel >= 5.8 required for ringbuf), %w", err)
	}
	defer coll.Close()

	for name, mp := range coll.Maps {
		p := filepath.Join(pinDir("maps"), name)
		if m.maps[name] != nil {
			continue // 已有 pinned 实例，丢弃新创建的
		}
		if err := mp.Pin(p); err != nil {
			return fmt.Errorf("error pin map %s, %w", name, err)
		}
		m.maps[name] = mp
		delete(coll.Maps, name) // 防止 coll.Close 关闭已 pin 的 fd
	}
	for name, prog := range coll.Programs {
		p := filepath.Join(pinDir("progs"), name)
		if m.progs[name] != nil {
			continue
		}
		if err := prog.Pin(p); err != nil {
			return fmt.Errorf("error pin prog %s, %w", name, err)
		}
		m.progs[name] = prog
		delete(coll.Programs, name)
	}
	return nil
}

// ---- map 访问 ----

func (m *Manager) podIPMap() *ebpf.Map    { return m.maps[MapPodIP] }
func (m *Manager) policyIDMap() *ebpf.Map { return m.maps[MapPolicyID] }
func (m *Manager) policyCIDRMap() *ebpf.Map {
	return m.maps[MapPolicyCIDR]
}
func (m *Manager) isolatedMap() *ebpf.Map { return m.maps[MapIsolated] }

// ---- Backend 接口实现 ----

func (m *Manager) UpsertPodIP(ip net.IP, identity uint32) error {
	mp := m.podIPMap()
	if mp == nil {
		return nil
	}
	return mp.Put(beIP(ip), PodIPValue{Identity: identity})
}

func (m *Manager) DeletePodIP(ip net.IP) error {
	mp := m.podIPMap()
	if mp == nil {
		return nil
	}
	return mp.Delete(beIP(ip))
}

func (m *Manager) WriteNodeConfig(nodeIP net.IP) error {
	mp := m.maps[MapNodeCfg]
	if mp == nil {
		return nil
	}
	return mp.Put(uint32(0), NodeValue{NodeIP: beIP(nodeIP)})
}

// EnsureENIAttached 按 ENI MAC 找宿主机设备并幂等挂载 ①② 程序。
// 由 daemon 的 reconcile 周期调用（设计 §7：程序缺失自愈）。
func (m *Manager) EnsureENIAttached(eniMACs []string) error {
	for _, mac := range eniMACs {
		idx, err := findENIIndexByMAC(mac)
		if err != nil {
			// ENI 未在节点上出现（未创建/已回收）是常态，跳过
			continue
		}
		if err := attachTC(m.progs[ProgMainIngress], idx, true, ProgMainIngress); err != nil {
			return fmt.Errorf("error attach main ingress on mac %s, %w", mac, err)
		}
		if err := attachTC(m.progs[ProgMainEgress], idx, false, ProgMainEgress); err != nil {
			return fmt.Errorf("error attach main egress on mac %s, %w", mac, err)
		}
	}
	return nil
}

// DrainEvents 消费 ringbuf 丢弃事件并打印（策略命中 metrics 于三期接入）。
func (m *Manager) DrainEvents(ctx context.Context) {
	mp := m.maps[MapEvents]
	if mp == nil {
		return
	}
	rd, err := ringbuf.NewReader(mp)
	if err != nil {
		llog.Logger.WithError(err).Error("ebpf: open ringbuf reader failed")
		return
	}
	defer rd.Close()

	go func() {
		<-ctx.Done()
		_ = rd.Close()
	}()

	for {
		rec, err := rd.Read()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			llog.Logger.WithError(err).Error("ebpf: read ringbuf failed")
			time.Sleep(time.Second)
			continue
		}
		logDropEvent(rec.RawSample)
	}
}

func logDropEvent(raw []byte) {
	if len(raw) < 24 {
		return
	}
	src := uint32ToIP(binary.BigEndian.Uint32(raw[0:4]))
	dst := uint32ToIP(binary.BigEndian.Uint32(raw[4:8]))
	srcID := binary.LittleEndian.Uint32(raw[8:12])
	dstID := binary.LittleEndian.Uint32(raw[12:16])
	sport := binary.LittleEndian.Uint16(raw[16:18])
	dport := binary.LittleEndian.Uint16(raw[18:20])
	proto := raw[20]
	hook := raw[21]
	reason := raw[22]

	reasons := map[byte]string{
		1: "spoof-src/dst", 2: "non-first-frag", 3: "policy-deny", 4: "no-identity",
	}
	llog.Logger.WithField("src", src).WithField("dst", dst).
		WithField("sport", sport).WithField("dport", dport).
		WithField("proto", proto).
		WithField("srcIdentity", srcID).WithField("dstIdentity", dstID).
		WithField("hook", hook).WithField("reason", reasons[reason]).
		Warn("ebpf: policy drop")
}

// ---- 供 policyagent 使用的底层写入口 ----

// ReplaceIsolated 全量替换隔离表
func (m *Manager) ReplaceIsolated(entries []ISOKey) error {
	mp := m.isolatedMap()
	if mp == nil {
		return nil
	}
	keep := make(map[ISOKey]bool, len(entries))
	for _, k := range entries {
		if err := mp.Put(k, uint8(1)); err != nil {
			return err
		}
		keep[k] = true
	}
	var stale []ISOKey
	var key ISOKey
	var val uint8
	it := mp.Iterate()
	for it.Next(&key, &val) {
		if !keep[key] {
			stale = append(stale, key)
		}
	}
	if it.Err() != nil {
		return it.Err()
	}
	for _, k := range stale {
		if err := mp.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// ReplacePair 全量替换身份对策略表
func (m *Manager) ReplacePair(keys []PairKey) error {
	mp := m.policyIDMap()
	if mp == nil {
		return nil
	}
	keep := make(map[PairKey]bool, len(keys))
	for _, k := range keys {
		if err := mp.Put(k, uint8(1)); err != nil {
			return err
		}
		keep[k] = true
	}
	var stale []PairKey
	var key PairKey
	it := mp.Iterate()
	for it.Next(&key, &struct{}{}) {
		if !keep[key] {
			stale = append(stale, key)
		}
	}
	if it.Err() != nil {
		return it.Err()
	}
	for _, k := range stale {
		if err := mp.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceCIDR 全量替换 CIDR 策略表
func (m *Manager) ReplaceCIDR(entries map[CIDRKey]CIDRValue) error {
	mp := m.policyCIDRMap()
	if mp == nil {
		return nil
	}
	keep := make(map[CIDRKey]bool, len(entries))
	for k, v := range entries {
		if err := mp.Put(k, v); err != nil {
			return err
		}
		keep[k] = true
	}
	var stale []CIDRKey
	var key CIDRKey
	var val CIDRValue
	it := mp.Iterate()
	for it.Next(&key, &val) {
		if !keep[key] {
			stale = append(stale, key)
		}
	}
	if it.Err() != nil {
		return it.Err()
	}
	for _, k := range stale {
		if err := mp.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

var _ Backend = (*Manager)(nil)
