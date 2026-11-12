package main

import (
	"fmt"
	"strings"
	"testing"
)

// miniTopology 是测试用的最小拓扑：一个 pod、一台 spine、一台 border、两个机架。
// 单 spine 意味着路径唯一，断言里能直接写出整条链路序列。
func miniTopology(t *testing.T) *Topology {
	t.Helper()
	tp := &Topology{
		Site: "test-site",
		Nodes: []Node{
			{Name: "border-01", Role: roleBorder, Pod: "core"},
			{Name: "spine-1", Role: roleSpine, Pod: "pod-a"},
			{Name: "tor-1", Role: roleTOR, Pod: "pod-a", Rack: "rack-1"},
			{Name: "tor-2", Role: roleTOR, Pod: "pod-a", Rack: "rack-2"},
			{Name: "srv-1a", Role: roleServer, Pod: "pod-a", Rack: "rack-1"},
			{Name: "srv-1b", Role: roleServer, Pod: "pod-a", Rack: "rack-1"},
			{Name: "srv-2a", Role: roleServer, Pod: "pod-a", Rack: "rack-2"},
			{Name: "srv-2b", Role: roleServer, Pod: "pod-a", Rack: "rack-2"},
		},
		Uplinks: []Uplink{
			{From: "spine-1", To: "border-01", BWGbps: 400},
			{From: "spine-1", To: "tor-1", BWGbps: 100},
			{From: "spine-1", To: "tor-2", BWGbps: 100},
			{From: "srv-1a", To: "tor-1", BWGbps: 25},
			{From: "srv-1b", To: "tor-1", BWGbps: 25},
			{From: "srv-2a", To: "tor-2", BWGbps: 25},
			{From: "srv-2b", To: "tor-2", BWGbps: 25},
		},
	}
	if err := tp.build(); err != nil {
		t.Fatalf("mini 拓扑构建失败：%v", err)
	}
	return tp
}

func TestLoadTopologySample(t *testing.T) {
	tp, err := LoadTopology("samples/topology.json")
	if err != nil {
		t.Fatalf("加载样本失败：%v", err)
	}
	if got := len(tp.Nodes); got != 36 {
		t.Fatalf("节点数 = %d，期望 36", got)
	}
	if got := len(tp.Links()); got != 44 {
		t.Fatalf("链路数 = %d，期望 44", got)
	}
	// 24 条接入 + 12 条 tor 上行 + 8 条 spine 上行。
	roles := map[string]int{}
	for _, n := range tp.Nodes {
		roles[n.Role]++
	}
	if roles[roleServer] != 24 || roles[roleTOR] != 6 || roles[roleSpine] != 4 || roles[roleBorder] != 2 {
		t.Fatalf("角色分布不对：%v", roles)
	}
}

func TestLinkKeyIsUndirected(t *testing.T) {
	if linkKey("tor-1", "spine-1") != linkKey("spine-1", "tor-1") {
		t.Fatal("链路的两个方向必须是同一个 key")
	}
	if got, want := linkKey("b", "a"), "a↔b"; got != want {
		t.Fatalf("linkKey = %q，期望 %q", got, want)
	}
}

func TestTopologyRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		nodes   []Node
		uplinks []Uplink
		wantErr string
	}{
		{
			name: "节点重名",
			nodes: []Node{
				{Name: "tor-1", Role: roleTOR, Rack: "rack-1"},
				{Name: "tor-1", Role: roleTOR, Rack: "rack-2"},
			},
			wantErr: "重名",
		},
		{
			name: "角色不认识",
			nodes: []Node{
				{Name: "gw-1", Role: "gateway"},
			},
			wantErr: "角色",
		},
		{
			name: "TOR 没写机架",
			nodes: []Node{
				{Name: "tor-1", Role: roleTOR},
			},
			wantErr: "必须写 rack",
		},
		{
			name: "服务器所在机架没有 TOR",
			nodes: []Node{
				{Name: "srv-1", Role: roleServer, Rack: "rack-9"},
			},
			wantErr: "没有 TOR",
		},
		{
			name: "跨层直连",
			nodes: []Node{
				{Name: "srv-1", Role: roleServer, Rack: "rack-1"},
				{Name: "tor-1", Role: roleTOR, Rack: "rack-1"},
				{Name: "spine-1", Role: roleSpine, Pod: "pod-a"},
			},
			uplinks: []Uplink{{From: "srv-1", To: "spine-1", BWGbps: 25}},
			wantErr: "跨了不止一层",
		},
		{
			name: "链路端点不存在",
			nodes: []Node{
				{Name: "tor-1", Role: roleTOR, Rack: "rack-1"},
			},
			uplinks: []Uplink{{From: "tor-1", To: "spine-x", BWGbps: 100}},
			wantErr: "不在节点表里",
		},
		{
			name: "服务器没连本机架 TOR",
			nodes: []Node{
				{Name: "tor-1", Role: roleTOR, Rack: "rack-1"},
				{Name: "tor-2", Role: roleTOR, Rack: "rack-2"},
				{Name: "srv-1", Role: roleServer, Rack: "rack-1"},
			},
			uplinks: []Uplink{{From: "srv-1", To: "tor-2", BWGbps: 25}},
			wantErr: "没连到本机架 TOR",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tp := &Topology{Nodes: c.nodes, Uplinks: c.uplinks}
			err := tp.build()
			if err == nil {
				t.Fatal("期望报错，实际通过了")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("错误信息 %q 里没有 %q", err, c.wantErr)
			}
		})
	}
}

func TestPseudoUnitSpread(t *testing.T) {
	// 相邻 seed（"pair|0" 到 "pair|59"）的输出不能聚在一起。
	// 这正是 FNV 直接取高位时的真实 bug：一个 12% 丢包的链路算出了 83% 的丢包率。
	low := 0
	for i := 0; i < 60; i++ {
		if pseudoUnit(fmt.Sprintf("%s|%d", "pair-seed", i)) < 0.5 {
			low++
		}
	}
	if low < 20 || low > 40 {
		t.Fatalf("60 个相邻 seed 里 %d 个落在下半区，分布明显不均", low)
	}
	// 同一个输入必须永远同一个输出，否则矩阵与结果都无法复现。
	if pseudoUnit("same") != pseudoUnit("same") {
		t.Fatal("同一个 seed 给出两个不同的值")
	}
	v := pseudoUnit("any")
	if v < 0 || v >= 1 {
		t.Fatalf("输出 %v 不在 [0,1) 区间", v)
	}
}

func TestPathOnMiniTopology(t *testing.T) {
	tp := miniTopology(t)
	cases := []struct {
		src, dst string
		want     []string
	}{
		{"srv-1a", "srv-2a", []string{"srv-1a↔tor-1", "spine-1↔tor-1", "spine-1↔tor-2", "srv-2a↔tor-2"}},
		{"srv-1a", "spine-1", []string{"srv-1a↔tor-1", "spine-1↔tor-1"}},
		{"srv-1a", "border-01", []string{"srv-1a↔tor-1", "spine-1↔tor-1", "border-01↔spine-1"}},
		{"tor-1", "tor-2", []string{"spine-1↔tor-1", "spine-1↔tor-2"}},
		{"border-01", "srv-2b", []string{"border-01↔spine-1", "spine-1↔tor-2", "srv-2b↔tor-2"}},
	}
	for _, c := range cases {
		got, err := tp.Path(c.src, c.dst)
		if err != nil {
			t.Fatalf("%s → %s 求路径失败：%v", c.src, c.dst, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%s → %s 路径 %v，期望 %v", c.src, c.dst, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s → %s 第 %d 段是 %s，期望 %s（整条 %v）", c.src, c.dst, i+1, got[i], c.want[i], got)
			}
		}
	}

	if _, err := tp.Path("srv-1a", "srv-1a"); err == nil {
		t.Fatal("两端同一个节点应该报错")
	}
	if _, err := tp.Path("srv-1a", "nope"); err == nil {
		t.Fatal("未知节点应该报错")
	}
}
