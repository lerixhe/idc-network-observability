package main

import (
	"os"
	"testing"
)

func mustTree(t *testing.T, nodes ...Node) *ObjectTree {
	t.Helper()
	tr, err := NewObjectTree(Tree{Nodes: nodes})
	if err != nil {
		t.Fatalf("建树失败: %v", err)
	}
	return tr
}

// demoNodes 是本文件共用的一棵小树：一个站点、一台设备、两个端口、一台主机、一个服务。
func demoNodes() []Node {
	return []Node{
		{ID: "site:room3", Kind: KindSite, Name: "room3", Aliases: []string{"ROOM3"}},
		{ID: "device:spine-01", Kind: KindDevice, Parent: "site:room3", Name: "spine-01",
			Aliases: []string{"SPINE-01", "10.20.0.11"}},
		{ID: "port:spine-01:Ethernet1/1", Kind: KindPort, Parent: "device:spine-01",
			Name: "spine-01:Ethernet1/1", Aliases: []string{"SPINE-01:Eth1/1", "spine-01:ethernet1/1"}},
		{ID: "port:spine-01:Ethernet1/2", Kind: KindPort, Parent: "device:spine-01",
			Name: "spine-01:Ethernet1/2", Aliases: []string{"spine-01:Eth1/2"}},
		{ID: "host:10.20.99.11", Kind: KindHost, Parent: "port:spine-01:Ethernet1/1", Name: "10.20.99.11"},
		{ID: "service:order-api@10.20.99.11", Kind: KindService, Parent: "host:10.20.99.11",
			Name: "order-api@10.20.99.11"},
	}
}

func TestResolveAliases(t *testing.T) {
	tree := mustTree(t, demoNodes()...)

	cases := []struct {
		raw  string
		want string
	}{
		{"device:spine-01", "device:spine-01"},
		{"SPINE-01", "device:spine-01"},
		{"  10.20.0.11  ", "device:spine-01"},
		{"SPINE-01:Eth1/1", "port:spine-01:Ethernet1/1"},
		{"spine-01:ethernet1/1", "port:spine-01:Ethernet1/1"},
		{"spine-01:Ethernet1/2", "port:spine-01:Ethernet1/2"},
		{"order-api@10.20.99.11", "service:order-api@10.20.99.11"},
		{"O R D E R", ""},
		{"acc-a3f-77:Eth1/2", ""},
	}
	for _, c := range cases {
		got, _ := tree.Resolve(c.raw)
		if got != c.want {
			t.Errorf("Resolve(%q) = %q，想要 %q", c.raw, got, c.want)
		}
	}
}

func TestAncestorsAndPath(t *testing.T) {
	tree := mustTree(t, demoNodes()...)

	got := tree.Ancestors("service:order-api@10.20.99.11")
	want := []string{"host:10.20.99.11", "port:spine-01:Ethernet1/1", "device:spine-01", "site:room3"}
	if len(got) != len(want) {
		t.Fatalf("祖先链 %v，想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("祖先链第 %d 层 %q，想要 %q", i, got[i], want[i])
		}
	}

	if p := tree.Path("service:order-api@10.20.99.11"); p !=
		"room3 > spine-01 > spine-01:Ethernet1/1 > 10.20.99.11 > order-api@10.20.99.11" {
		t.Errorf("路径 %q", p)
	}
}

// 同层与兄弟都不是祖先。让它们互压就是把两个独立故障合成一个。
func TestSiblingsAreNotAncestors(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	a := "port:spine-01:Ethernet1/1"
	b := "port:spine-01:Ethernet1/2"
	if tree.IsAncestor(a, b) || tree.IsAncestor(b, a) {
		t.Fatal("同层端口被判成祖先关系")
	}
	if tree.IsAncestor(a, a) {
		t.Fatal("节点被判成自己的祖先")
	}
	if !tree.IsAncestor("device:spine-01", a) {
		t.Fatal("设备没被判成端口的祖先")
	}
}

// 脏树必须当场拒收。留到抑制阶段，它会变成一条永远被压的告警。
func TestNewObjectTreeRejectsDirtyData(t *testing.T) {
	cases := []struct {
		name  string
		nodes []Node
	}{
		{"父不存在", []Node{
			{ID: "device:d1", Kind: KindDevice, Parent: "site:ghost", Name: "d1"},
		}},
		{"ID 重复", []Node{
			{ID: "device:d1", Kind: KindDevice, Name: "d1"},
			{ID: "device:d1", Kind: KindDevice, Name: "d1b"},
		}},
		{"kind 不认识", []Node{
			{ID: "rack:r1", Kind: "rack", Name: "r1"},
		}},
		{"别名撞车", []Node{
			{ID: "device:d1", Kind: KindDevice, Name: "d1", Aliases: []string{"core"}},
			{ID: "device:d2", Kind: KindDevice, Name: "d2", Aliases: []string{"CORE"}},
		}},
	}
	for _, c := range cases {
		if _, err := NewObjectTree(Tree{Nodes: c.nodes}); err == nil {
			t.Errorf("%s：应当拒收，但收下了", c.name)
		}
	}
}

// 环是脏数据的常态。追祖先链必须能停下来，而不是把进程挂死。
func TestAncestorsStopsOnCycle(t *testing.T) {
	tree := &ObjectTree{
		nodes: map[string]Node{
			"device:a": {ID: "device:a", Kind: KindDevice, Parent: "device:b", Name: "a"},
			"device:b": {ID: "device:b", Kind: KindDevice, Parent: "device:a", Name: "b"},
		},
	}
	got := tree.Ancestors("device:a")
	if len(got) > 2 {
		t.Fatalf("环没截断，祖先链 %v", got)
	}
}

func TestLoadAlertsRejectsBadTimestamps(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/a.jsonl"
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("写样本失败: %v", err)
		}
	}

	write(`{"id":"a1","ts":"2026-05-12T10:00:00Z","obj":"device:d1","class":"port_down","severity":"critical","truth":"child"}` + "\n")
	if _, err := loadAlerts(path); err != nil {
		t.Fatalf("正常样本被拒: %v", err)
	}

	write(`{"id":"a1","ts":"昨天","obj":"device:d1","class":"port_down","severity":"critical","truth":"child"}` + "\n")
	if _, err := loadAlerts(path); err == nil {
		t.Fatal("坏时间戳应当拒收")
	}

	write(`{"id":"a1","ts":"2026-05-12T10:00:00Z","resolve_ts":"2026-05-12T09:00:00Z","obj":"device:d1","class":"port_down","severity":"critical","truth":"child"}` + "\n")
	if _, err := loadAlerts(path); err == nil {
		t.Fatal("恢复早于发生应当拒收")
	}

	write(`{"id":"a1","ts":"2026-05-12T10:00:00Z","obj":"device:d1","class":"port_down","severity":"critical","truth":"maybe"}` + "\n")
	if _, err := loadAlerts(path); err == nil {
		t.Fatal("不认识的 truth 应当拒收")
	}
}
