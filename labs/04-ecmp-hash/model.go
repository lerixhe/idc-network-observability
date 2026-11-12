package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
)

// Tuple 是 ECMP 哈希的输入：IDC 里最常见的五元组。
//
// 真实设备参与哈希的字段因厂商和型号而异——有的只取源/目的 IP，有的把
// L4 端口算上，有的连 VLAN、MAC 一起搅。这里固定成三项 IP 层加两项 L4，
// 因为它覆盖了绝大多数 TOR/Spine 的缺省配置（第 12 课）。
type Tuple struct {
	SrcIP   netip.Addr
	DstIP   netip.Addr
	Proto   uint8
	SrcPort uint16
	DstPort uint16
}

// encode 把五元组拍成固定 13 字节：源 IP(4) 目的 IP(4) 协议(1) 源端口(2) 目的端口(2)。
//
// 字段顺序是哈希口径的一部分。改一行顺序，全部落桶结果就变了——这也正是
// 不同厂商设备对同一批五元组给出不同路径的原因，所以顺序写死并测试住。
func (t Tuple) encode() []byte {
	b := make([]byte, 0, 13)
	src, dst := t.SrcIP.As4(), t.DstIP.As4()
	b = append(b, src[:]...)
	b = append(b, dst[:]...)
	b = append(b, t.Proto)
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], t.SrcPort)
	b = append(b, port[:]...)
	binary.BigEndian.PutUint16(port[:], t.DstPort)
	b = append(b, port[:]...)
	return b
}

// String 是人读形式，进终端表格。
func (t Tuple) String() string {
	return fmt.Sprintf("%s:%d → %s:%d/%d", t.SrcIP, t.SrcPort, t.DstIP, t.DstPort, t.Proto)
}

// Member 是一条等价路径，也就是 ECMP 组里的一个下一跳。
//
// ID 只用 ASCII：它进终端表格和 CSV。中文说明放 Desc。
type Member struct {
	ID   string `json:"id"`
	Desc string `json:"desc"`
}

// PathSet 是一组等价路径：一条路由前缀加上它的全部下一跳。
//
// HashSalt 是设备侧的哈希盐（有的厂商叫 hash seed / hash polynomial）。
// 同一批五元组，盐不同落桶全变——这是「换一台设备就得重测」的物理原因。
type PathSet struct {
	Name     string
	Route    string
	HashSalt uint32
	Base     Tuple
	Members  []Member
}

// TupleWithPort 把固定的四个字段与给定源端口拼成完整五元组。
// 探测手段的本质就是「换个源端口，看落到哪条 member」，所以只有一个字段可变。
func (s PathSet) TupleWithPort(port uint16) Tuple {
	t := s.Base
	t.SrcPort = port
	return t
}

type rawPathSet struct {
	Name     string   `json:"name"`
	Route    string   `json:"route"`
	HashSalt uint32   `json:"hash_salt"`
	Base     rawTuple `json:"base"`
	Members  []Member `json:"members"`
}

// rawTuple 是样本里的五元组，缺源端口——源端口由探测方式决定，不写在样本里。
type rawTuple struct {
	SrcIP   string `json:"src_ip"`
	DstIP   string `json:"dst_ip"`
	Proto   int    `json:"proto"`
	DstPort int    `json:"dst_port"`
}

// loadPathSets 读等价路径样本（JSON 数组）。
//
// 校验一律走严格口径：IP 只收 IPv4，member 少于 2 条报错。样本脏了还硬跑，
// 后面三张表全是假数据，而且看起来完全正常。
func loadPathSets(path string) ([]PathSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw []rawPathSet
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s: 样本里没有任何一组等价路径", path)
	}

	sets := make([]PathSet, 0, len(raw))
	for i, r := range raw {
		src, err := netip.ParseAddr(r.Base.SrcIP)
		if err != nil || !src.Is4() {
			return nil, fmt.Errorf("%s 第 %d 组: src_ip %q 不是 IPv4", path, i+1, r.Base.SrcIP)
		}
		dst, err := netip.ParseAddr(r.Base.DstIP)
		if err != nil || !dst.Is4() {
			return nil, fmt.Errorf("%s 第 %d 组: dst_ip %q 不是 IPv4", path, i+1, r.Base.DstIP)
		}
		if r.Base.Proto < 1 || r.Base.Proto > 255 {
			return nil, fmt.Errorf("%s 第 %d 组: proto %d 不在 1–255", path, i+1, r.Base.Proto)
		}
		if r.Base.DstPort < 1 || r.Base.DstPort > 65535 {
			return nil, fmt.Errorf("%s 第 %d 组: dst_port %d 不在 1–65535", path, i+1, r.Base.DstPort)
		}
		if len(r.Members) < 2 {
			return nil, fmt.Errorf("%s 第 %d 组: 只有 %d 条 member，等价路径至少要两条",
				path, i+1, len(r.Members))
		}
		seen := make(map[string]bool, len(r.Members))
		for _, m := range r.Members {
			if m.ID == "" {
				return nil, fmt.Errorf("%s 第 %d 组: 有 member 没写 id", path, i+1)
			}
			if seen[m.ID] {
				return nil, fmt.Errorf("%s 第 %d 组: member id %q 重复", path, i+1, m.ID)
			}
			seen[m.ID] = true
		}
		sets = append(sets, PathSet{
			Name:     r.Name,
			Route:    r.Route,
			HashSalt: r.HashSalt,
			Base: Tuple{
				SrcIP:   src,
				DstIP:   dst,
				Proto:   uint8(r.Base.Proto),
				DstPort: uint16(r.Base.DstPort),
			},
			Members: r.Members,
		})
	}
	return sets, nil
}
