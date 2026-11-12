package main

import (
	"testing"
	"time"
)

// loadSamples 加载仓库里那份样本，多数测试直接在这份数据上跑。
func loadSamples(t *testing.T) *Dataset {
	t.Helper()
	ds, err := LoadDataset(
		"samples/arp.jsonl",
		"samples/fdb.jsonl",
		"samples/dhcp-leases.jsonl",
		"samples/ports.json",
	)
	if err != nil {
		t.Fatalf("加载样本失败: %v", err)
	}
	return ds
}

// miniDataset 是一份最小的合成数据，用来构造样本里不方便出现的形态。
func miniDataset() *Dataset {
	ts := time.Date(2026, 5, 12, 10, 12, 0, 0, time.UTC)
	inv := &Inventory{
		Devices: []Device{{Name: "sw1", Role: "leaf"}},
		Ports: []PortRecord{
			{Device: "sw1", Port: "Po1", Desc: "bond0", Members: []string{"Ethernet1/1", "Ethernet1/2"}},
			{Device: "sw1", Port: "Ethernet1/1", Rack: "A-01", U: 10},
			{Device: "sw1", Port: "Ethernet1/2", Rack: "A-02", U: 20},
		},
	}
	inv.build()
	return &Dataset{
		ARP: []ArpEntry{{
			TS: ts, Gateway: "gw", VLAN: 10, IfName: "Vlanif10", IP: "10.0.0.5",
			MAC: "00:11:22:33:44:55", State: "reachable", LastSeen: ts,
		}},
		FDB: []FdbEntry{{
			TS: ts, Switch: "sw1", VLAN: 10, MAC: "0011.2233.4455", Port: "Po1", AgeS: 10,
		}},
		Inv: inv,
		TS:  ts,
	}
}

// 三厂商三种写法必须归一到同一个 MAC，否则三张表永远是三个东西。
func TestNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"00:50:56:aa:00:44": "00:50:56:aa:00:44",
		"0050-56aa-0044":    "00:50:56:aa:00:44",
		"0050.56aa.0044":    "00:50:56:aa:00:44",
		"00:50:56:AA:00:44": "00:50:56:aa:00:44",
		"  0050.56AA.0044 ": "00:50:56:aa:00:44",
		"":                  "",
		"not-a-mac":         "not-a-mac", // 认不出就原样返回，不许吞成空串
	}
	for in, want := range cases {
		if got := normalizeMAC(in); got != want {
			t.Errorf("normalizeMAC(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func TestLoadDatasetSizes(t *testing.T) {
	ds := loadSamples(t)
	if len(ds.ARP) != 10 {
		t.Errorf("ARP 条数 = %d，想要 10", len(ds.ARP))
	}
	if len(ds.FDB) != 17 {
		t.Errorf("FDB 条数 = %d，想要 17", len(ds.FDB))
	}
	if len(ds.Leases) != 10 {
		t.Errorf("租约行数 = %d，想要 10", len(ds.Leases))
	}
	if len(ds.Inv.Ports) != 18 {
		t.Errorf("台账端口数 = %d，想要 18", len(ds.Inv.Ports))
	}
	if got, want := ds.TS.Format(time.RFC3339), "2026-05-12T10:12:00Z"; got != want {
		t.Errorf("查询时刻 = %s，想要 %s", got, want)
	}
	if ds.switchCount() != 4 {
		t.Errorf("二层设备数 = %d，想要 4", ds.switchCount())
	}
	if ds.gatewayCount() != 2 {
		t.Errorf("三层设备数 = %d，想要 2", ds.gatewayCount())
	}
}

// 租约日志是事件流，同一 IP 最后一行才算数。
func TestLeaseStateLastEventWins(t *testing.T) {
	ds := loadSamples(t)

	ok, found := ds.leaseState("10.32.7.18", ds.TS)
	if !found || !ok.Active {
		t.Fatalf("10.32.7.18 的租约应有效，实际 found=%v active=%v", found, ok.Active)
	}

	released, found := ds.leaseState("10.32.8.90", ds.TS)
	if !found || released.Active {
		t.Fatalf("10.32.8.90 最后一行是 release，租约应无效")
	}
	if released.Reason != "客户端已释放" {
		t.Errorf("原因 = %q，想要「客户端已释放」", released.Reason)
	}

	expired, found := ds.leaseState("10.33.1.20", ds.TS)
	if !found || expired.Active {
		t.Fatalf("10.33.1.20 最后一行是 expire，租约应无效")
	}

	if _, found := ds.leaseState("10.32.7.99", ds.TS); found {
		t.Error("10.32.7.99 不该有租约")
	}
}

func TestInactiveLeases(t *testing.T) {
	bad := loadSamples(t).inactiveLeases()
	if len(bad) != 2 {
		t.Fatalf("没生效的租约 = %d 个，想要 2 个：%v", len(bad), bad)
	}
}

func TestIsAggregatePortName(t *testing.T) {
	cases := map[string]bool{
		"Port-Channel10":       true,
		"Eth-Trunk3":           true,
		"po10":                 true,
		"Bridge-Aggregation11": true,
		"ae5":                  true,
		"Ethernet1/7":          false,
		"Port-Channel":         false,
		"PowerSupply1":         false,
		"Vlanif10":             false,
	}
	for in, want := range cases {
		if got := isAggregatePortName(in); got != want {
			t.Errorf("isAggregatePortName(%q) = %v，想要 %v", in, got, want)
		}
	}
}

// 台账里的端口名大小写不该决定查不查得到。
func TestInventoryPortLookup(t *testing.T) {
	inv := loadSamples(t).Inv
	if _, ok := inv.Port("leaf-a3f-01", "Ethernet1/7"); !ok {
		t.Fatal("Ethernet1/7 应该查得到")
	}
	if _, ok := inv.Port("LEAF-A3F-01", "ethernet1/7"); !ok {
		t.Fatal("大小写不同也该查得到")
	}
	if _, ok := inv.Port("leaf-b1f-01", "Ethernet1/40"); ok {
		t.Fatal("Ethernet1/40 台账里没有，这是样本故意留的缺口")
	}
}

func TestDurText(t *testing.T) {
	cases := map[time.Duration]string{
		12 * time.Second:  "12s",
		720 * time.Second: "12m",
		750 * time.Second: "12m30s",
		618 * time.Minute: "10h18m",
		2 * time.Hour:     "2h",
		-5 * time.Second:  "0s",
	}
	for in, want := range cases {
		if got := durText(in); got != want {
			t.Errorf("durText(%s) = %q，想要 %q", in, got, want)
		}
	}
}
