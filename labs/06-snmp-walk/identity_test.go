package main

import "testing"

// before/after 是两轮真实采集的结果：一轮原始样本，一轮换板重启后的样本。
func reindexTables(t *testing.T, device string) (*PortTable, *PortTable) {
	t.Helper()
	before := collectOnce(t, "samples/fleet.json", 1, nil)
	after := collectOnce(t, "samples/fleet-rebooted.json", 1, nil)
	tb, ok := before.PortTable(device)
	if !ok {
		t.Fatalf("上一轮没采到 %s", device)
	}
	ta, ok := after.PortTable(device)
	if !ok {
		t.Fatalf("重启后没采到 %s", device)
	}
	if tb.Anonymous() != 0 || ta.Anonymous() != 0 {
		t.Fatalf("%s 有行缺 ifName，后面的对接断言就没意义了", device)
	}
	return tb, ta
}

// 核心失败案例：设备换板重启后 ifIndex 重排，按 ifIndex 建的主键把历史接到了别的端口上。
func TestJoinByIndexAttachesHistoryToWrongPort(t *testing.T) {
	tb, ta := reindexTables(t, "tor-a3f-01")
	rep := JoinPorts(tb, ta, JoinByIndex)

	if len(rep.Pairs) != 16 {
		t.Fatalf("按 ifIndex 接上 %d 条，期望 16（24 个端口里 8 个换了索引位置）", len(rep.Pairs))
	}
	if got := rep.WrongObjects(); got != 16 {
		t.Fatalf("接错对象 %d 条，期望 16——接上的每一条都指向了别的端口", got)
	}
	if len(rep.Dropped) != 8 {
		t.Fatalf("断线 %d 条，期望 8", len(rep.Dropped))
	}
	if len(rep.Fresh) != 8 {
		t.Fatalf("新增 %d 条，期望 8", len(rep.Fresh))
	}
	// 最假的一条：原来 10G 服务器口的位置上，现在挂着 100G 上联。
	jumps := rep.BigJumps(4)
	if len(jumps) != 4 {
		t.Fatalf("速率差 ≥ 4 倍的对接 %d 条，期望 4", len(jumps))
	}
	for _, p := range jumps {
		if p.Before.SpeedMbps() == p.After.SpeedMbps() {
			t.Fatalf("速率比算错: %d vs %d", p.Before.SpeedMbps(), p.After.SpeedMbps())
		}
		if p.Before.Alias == p.After.Alias {
			t.Fatalf("速率差 10 倍但 alias 相同，样本有问题: %s", p.Before.Alias)
		}
	}
}

// 同一个索引在新一轮指向了 uplink，历史曲线的量级会直接跳一个数量级。
func TestReindexChangesSpeedClass(t *testing.T) {
	tb, ta := reindexTables(t, "tor-a3f-01")
	list := Reindexes(tb, ta)
	if len(list) != 16 {
		t.Fatalf("重排点 %d 个，期望 16", len(list))
	}
	ratio10 := 0
	for _, r := range list {
		if r.SpeedRatio >= 10 {
			ratio10++
		}
		if r.Before.Name == r.After.Name {
			t.Fatalf("ifIndex %d 前后是同一个端口，不该报重排", r.IfIndex)
		}
	}
	if ratio10 != 4 {
		t.Fatalf("速率差 10 倍的重排点 = %d，期望 4", ratio10)
	}
	// 排序稳定，报表才能对齐。
	for i := 1; i < len(list); i++ {
		if list[i-1].IfIndex > list[i].IfIndex {
			t.Fatalf("重排点没按 ifIndex 排序: %d 在 %d 前面", list[i-1].IfIndex, list[i].IfIndex)
		}
	}
}

// 同一份数据换成 ifName 做主键，一条都不错。
func TestJoinByNameKeepsHistoryOnSamePort(t *testing.T) {
	tb, ta := reindexTables(t, "tor-a3f-01")
	rep := JoinPorts(tb, ta, JoinByName)
	if len(rep.Pairs) != 24 {
		t.Fatalf("按 ifName 接上 %d 条，期望 24", len(rep.Pairs))
	}
	if got := rep.WrongObjects(); got != 0 {
		t.Fatalf("按 ifName 对接不该错对象，实际 %d 条", got)
	}
	if len(rep.Dropped) != 0 || len(rep.Fresh) != 0 {
		t.Fatalf("按 ifName 不该断线/新增：断 %d 新 %d", len(rep.Dropped), len(rep.Fresh))
	}
	moved := 0
	for _, p := range rep.Pairs {
		if p.Before.IfIndex != p.After.IfIndex {
			moved++
		}
	}
	// 24 个端口的 ifIndex 全变了，对象一个没错——这就是「句柄变了、主键没变」。
	if moved != 24 {
		t.Fatalf("有 %d 个端口的 ifIndex 变了，期望 24", moved)
	}
}

// ifName 也不是万能主键：换主控板后槽位号变了，整表断线，只能靠 alias 接回。
func TestJoinByNameBreaksWhenSlotNumberChanges(t *testing.T) {
	tb, ta := reindexTables(t, "leaf-legacy-01")
	byName := JoinPorts(tb, ta, JoinByName)
	if len(byName.Pairs) != 0 {
		t.Fatalf("ifName 从 GE0/0/x 变成 GE0/1/x，应该一条都接不上，实际接上 %d 条", len(byName.Pairs))
	}
	if len(byName.Dropped) != 12 {
		t.Fatalf("断线 %d 条，期望 12", len(byName.Dropped))
	}
	if byName.Dropped[0].Name != "GE0/0/1" {
		t.Fatalf("断线的第一条应是 GE0/0/1，实际 %s", byName.Dropped[0].Name)
	}

	// alias 是兜底：同一台设备用 alias 对接，12 条全部接回，且没错对象。
	byAlias := JoinPorts(tb, ta, JoinByAlias)
	if len(byAlias.Pairs) != 12 {
		t.Fatalf("按 ifAlias 接上 %d 条，期望 12", len(byAlias.Pairs))
	}
	if got := byAlias.WrongObjects(); got != 0 {
		t.Fatalf("按 ifAlias 不该错对象，实际 %d", got)
	}
	// 但索引没变，说明这台设备只是改名，不是重排。
	if list := Reindexes(tb, ta); len(list) != 0 {
		t.Fatalf("这台设备 ifIndex 没重排，不该报重排点，实际 %d 个", len(list))
	}
}

// 没有 alias 的端口连兜底都没有：样本里 tor-a3f-02 故意留了两个。
func TestUnnamedPortsCannotFallBack(t *testing.T) {
	c := collectOnce(t, "samples/fleet.json", 1, nil)
	table, ok := c.PortTable("tor-a3f-02")
	if !ok {
		t.Fatal("没采到 tor-a3f-02")
	}
	if got := table.Unnamed(); got != 2 {
		t.Fatalf("没有 alias 的端口 = %d，期望 2", got)
	}
	ports := table.Ports
	empty := 0
	for _, p := range ports {
		if p.Alias == "" {
			empty++
			if _, ok := table.ByAlias(""); ok {
				t.Fatal("空 alias 不该进索引：查空串会随机命中一个端口")
			}
		}
	}
	if empty != 2 {
		t.Fatalf("空 alias 端口 = %d，期望 2", empty)
	}
}

// 主键必须带设备名：每台设备的 ifName 都一样，只按 ifName 建键会互相覆盖。
func TestPortKeyIncludesDevice(t *testing.T) {
	p := Port{Device: "spine-01", Name: "Ethernet1/1"}
	if got := p.Key(); got != "spine-01/Ethernet1/1" {
		t.Fatalf("主键 = %q", got)
	}
	other := Port{Device: "spine-02", Name: "Ethernet1/1"}
	if p.Key() == other.Key() {
		t.Fatal("两台设备的 Ethernet1/1 不能是同一个键")
	}
}

// 速率优先取 ifHighSpeed：ifSpeed 在 10G 以上就顶格，读出来分不清 10G 和 100G。
func TestPortSpeedPrefersIfHighSpeed(t *testing.T) {
	p := Port{SpeedBps: 4294967295, HighSpeedMbps: 100000}
	if got := p.SpeedMbps(); got != 100000 {
		t.Fatalf("速率 = %d，期望 100000", got)
	}
	// 老设备没有 ifXTable 时退回 ifSpeed。
	q := Port{SpeedBps: 1_000_000_000}
	if got := q.SpeedMbps(); got != 1000 {
		t.Fatalf("退回 ifSpeed 的速率 = %d，期望 1000", got)
	}
}

func TestPortTableLookups(t *testing.T) {
	table := NewPortTable("tor-a3f-01", []Port{
		{Device: "tor-a3f-01", IfIndex: 5, Name: "Ethernet1/5", Alias: "to-srv-05"},
		{Device: "tor-a3f-01", IfIndex: 6, Name: "Ethernet1/6", Alias: "to-srv-05"}, // alias 重复
		{Device: "tor-a3f-01", IfIndex: 7, Name: "", Alias: ""},
	})
	if p, ok := table.ByIndex(5); !ok || p.Name != "Ethernet1/5" {
		t.Fatalf("按 ifIndex 查 5 得到 %+v", p)
	}
	if p, ok := table.ByName("Ethernet1/6"); !ok || p.IfIndex != 6 {
		t.Fatalf("按 ifName 查得到 %+v", p)
	}
	if table.DupAlias != 1 {
		t.Fatalf("重复 alias 计数 = %d，期望 1", table.DupAlias)
	}
	if table.Anonymous() != 1 {
		t.Fatalf("匿名行数 = %d，期望 1", table.Anonymous())
	}
	if _, ok := table.ByName(""); ok {
		t.Fatal("空 ifName 不该进索引")
	}
}
