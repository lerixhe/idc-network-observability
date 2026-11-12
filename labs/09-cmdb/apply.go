package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Apply 把补丁应用在一份台账副本上，返回新的台账对象。入参不被修改。
//
// 这里是整个实验唯一「写入」台账的地方，它有两个硬约束：
//
//  1. 只吃内存里的台账对象，返回值还是一份对象——写文件那一步在外面，
//     由 main 决定写到哪里，而且不允许写到台账本身的路径上（guardWritePath）。
//  2. 每条补丁的字段走白名单，出现白名单以外的字段直接报错。
//     静默忽略一个字段，比报错危险得多：人以为改过了。
func Apply(ledger *Ledger, patches []Patch) (*Ledger, error) {
	out, err := cloneLedger(ledger)
	if err != nil {
		return nil, err
	}
	for _, p := range patches {
		if err := applyPatch(out, p); err != nil {
			return nil, fmt.Errorf("补丁 %s（%s %s）: %w", p.ID, p.Op, p.Target, err)
		}
	}
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("应用补丁之后台账不再合法: %w", err)
	}
	return out, nil
}

func cloneLedger(l *Ledger) (*Ledger, error) {
	data, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	var out Ledger
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func applyPatch(l *Ledger, p Patch) error {
	allowed, known := patchFields[p.Op]
	if !known {
		return fmt.Errorf("不认识的 op %q", p.Op)
	}
	for field := range p.Set {
		if !containsString(allowed, field) {
			return fmt.Errorf("op %s 不允许写字段 %q", p.Op, field)
		}
	}

	switch p.Op {
	case OpDeviceLifecycle:
		lifecycle, ok := p.Set["lifecycle"]
		if !ok {
			return fmt.Errorf("缺 lifecycle")
		}
		switch lifecycle {
		case LifecycleActive, LifecycleStaging, LifecycleRetired:
		default:
			return fmt.Errorf("lifecycle=%q 不是合法取值", lifecycle)
		}
		d, err := deviceOf(l, p.Target)
		if err != nil {
			return err
		}
		d.Lifecycle = lifecycle

	case OpDeviceAlias:
		alias, ok := p.Set["alias_of"]
		if !ok || alias == "" {
			return fmt.Errorf("缺 alias_of")
		}
		if alias == p.Target {
			return fmt.Errorf("alias_of 指向自己")
		}
		if _, err := deviceOf(l, alias); err != nil {
			return fmt.Errorf("alias_of 指向的设备不存在: %w", err)
		}
		d, err := deviceOf(l, p.Target)
		if err != nil {
			return err
		}
		d.AliasOf = alias

	case OpDeviceCreate:
		hostname, ok := p.Set["hostname"]
		if !ok || hostname == "" {
			return fmt.Errorf("缺 hostname")
		}
		if hostname != p.Target {
			return fmt.Errorf("hostname %q 与 target %q 不一致", hostname, p.Target)
		}
		if _, err := deviceOf(l, hostname); err == nil {
			return fmt.Errorf("设备 %s 已经登记过了", hostname)
		}
		d := Device{
			Hostname:  hostname,
			MgmtIP:    p.Set["mgmt_ip"],
			Site:      p.Set["site"],
			Owner:     p.Set["owner"],
			Lifecycle: p.Set["lifecycle"],
		}
		if d.Lifecycle == "" {
			d.Lifecycle = LifecycleStaging
		}
		l.Devices = append(l.Devices, d)

	case OpLinkPeer:
		peerDevice, ok1 := p.Set["peer_device"]
		peerPort, ok2 := p.Set["peer_port"]
		if !ok1 || !ok2 {
			return fmt.Errorf("缺 peer_device 或 peer_port")
		}
		if _, err := deviceOf(l, peerDevice); err != nil {
			return fmt.Errorf("对端设备不存在: %w", err)
		}
		link, err := linkOf(l, p.Target)
		if err != nil {
			return err
		}
		link.PeerDevice = peerDevice
		link.PeerPort = peerPort

	case OpLinkLocalPort:
		port, ok := p.Set["port"]
		if !ok || port == "" {
			return fmt.Errorf("缺 port")
		}
		link, err := linkOf(l, p.Target)
		if err != nil {
			return err
		}
		if port == link.Port {
			return fmt.Errorf("本端端口名没变")
		}
		for _, other := range l.Links {
			if other.Device == link.Device && other.Port == port {
				return fmt.Errorf("%s#%s 已经有声明了，改过去会撞车", link.Device, port)
			}
		}
		link.Port = port

	case OpLinkIfIndex:
		raw, ok := p.Set["if_index"]
		if !ok {
			return fmt.Errorf("缺 if_index")
		}
		ifIndex, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("if_index=%q 不是整数", raw)
		}
		link, err := linkOf(l, p.Target)
		if err != nil {
			return err
		}
		link.IfIndex = ifIndex

	case OpLinkCreate:
		k, err := splitTarget(p.Target)
		if err != nil {
			return err
		}
		peerDevice, ok1 := p.Set["peer_device"]
		peerPort, ok2 := p.Set["peer_port"]
		if !ok1 || !ok2 {
			return fmt.Errorf("缺 peer_device 或 peer_port")
		}
		if _, err := deviceOf(l, k.device); err != nil {
			return fmt.Errorf("本端设备不存在: %w", err)
		}
		if _, err := deviceOf(l, peerDevice); err != nil {
			return fmt.Errorf("对端设备不存在: %w", err)
		}
		if _, err := linkOf(l, p.Target); err == nil {
			return fmt.Errorf("%s 已经有端口声明了", p.Target)
		}
		decl := PortDecl{Device: k.device, Port: k.port, PeerDevice: peerDevice, PeerPort: peerPort}
		if raw, ok := p.Set["if_index"]; ok {
			ifIndex, err := strconv.Atoi(raw)
			if err != nil {
				return fmt.Errorf("if_index=%q 不是整数", raw)
			}
			decl.IfIndex = ifIndex
		}
		l.Links = append(l.Links, decl)

	case OpLinkDelete:
		for i, link := range l.Links {
			k := devPort{device: link.Device, port: link.Port}
			if k.String() != p.Target {
				continue
			}
			l.Links = append(l.Links[:i], l.Links[i+1:]...)
			return nil
		}
		return fmt.Errorf("台账里没有这条端口声明")

	default:
		return fmt.Errorf("op %q 没有实现", p.Op)
	}
	return nil
}

func deviceOf(l *Ledger, hostname string) (*Device, error) {
	for i := range l.Devices {
		if l.Devices[i].Hostname == hostname {
			return &l.Devices[i], nil
		}
	}
	return nil, fmt.Errorf("台账里没有设备 %s", hostname)
}

func linkOf(l *Ledger, target string) (*PortDecl, error) {
	for i := range l.Links {
		k := devPort{device: l.Links[i].Device, port: l.Links[i].Port}
		if k.String() == target {
			return &l.Links[i], nil
		}
	}
	return nil, fmt.Errorf("台账里没有 %s 的端口声明", target)
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// guardWritePath 是那把锁：补丁可以写出去，但绝不能写回台账本身。
//
// 真正的落库要走人工评审、变更窗口、备份，一步都不能少。实验只到
// 「生成一份可以直接 diff 的副本」这一步，剩下的交给流程。
func guardWritePath(ledgerPath, writePath string) error {
	if writePath == "" {
		return fmt.Errorf("没有指定写库路径")
	}
	a, err := filepath.Abs(ledgerPath)
	if err != nil {
		return err
	}
	b, err := filepath.Abs(writePath)
	if err != nil {
		return err
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return fmt.Errorf("拒绝写入 %s：这是台账本身。补丁只能写到副本，落库走人工确认", ledgerPath)
	}
	return nil
}

// WriteLedger 把台账落成可读的 JSON 文件。
func WriteLedger(path string, l *Ledger) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}
