package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFleetSamples(t *testing.T) {
	f, err := LoadFleet("samples/fleet.json")
	if err != nil {
		t.Fatalf("加载样本失败: %v", err)
	}
	if len(f.Devices) != 5 {
		t.Fatalf("设备数 = %d，期望 5", len(f.Devices))
	}
	if got := totalPorts(f); got != 100 {
		t.Fatalf("端口总数 = %d，期望 100", got)
	}
	// 设备按名字排序，采集顺序才稳定。
	for i := 1; i < len(f.Devices); i++ {
		if f.Devices[i-1].Name > f.Devices[i].Name {
			t.Fatalf("设备没按名字排序: %s 在 %s 前面", f.Devices[i-1].Name, f.Devices[i].Name)
		}
	}
	tiers := map[string]string{}
	for _, d := range f.Devices {
		tiers[d.Name] = d.SNMP.Tier
	}
	want := map[string]string{
		"spine-01": "A", "tor-a3f-01": "B", "tor-a3f-02": "C",
		"leaf-legacy-01": "C", "tor-b7f-01": "B",
	}
	for name, tier := range want {
		if tiers[name] != tier {
			t.Fatalf("%s 档位 = %q，期望 %q", name, tiers[name], tier)
		}
	}
}

func TestFleetValidate(t *testing.T) {
	cases := []struct {
		name string
		f    Fleet
	}{
		{"没有设备", Fleet{}},
		{"设备重名", Fleet{Devices: []Device{
			{Name: "a", SNMP: AgentProfile{MaxRepetitions: 8}, Interfaces: []Interface{{IfIndex: 1, IfName: "x"}}},
			{Name: "a", SNMP: AgentProfile{MaxRepetitions: 8}, Interfaces: []Interface{{IfIndex: 1, IfName: "x"}}},
		}}},
		{"没有端口", Fleet{Devices: []Device{{Name: "a", SNMP: AgentProfile{MaxRepetitions: 8}}}}},
		{"ifIndex 重复", Fleet{Devices: []Device{{Name: "a", SNMP: AgentProfile{MaxRepetitions: 8},
			Interfaces: []Interface{{IfIndex: 3, IfName: "x"}, {IfIndex: 3, IfName: "y"}}}}}},
		{"ifIndex 为 0", Fleet{Devices: []Device{{Name: "a", SNMP: AgentProfile{MaxRepetitions: 8},
			Interfaces: []Interface{{IfIndex: 0, IfName: "x"}}}}}},
		{"没有 ifName", Fleet{Devices: []Device{{Name: "a", SNMP: AgentProfile{MaxRepetitions: 8},
			Interfaces: []Interface{{IfIndex: 1}}}}}},
		{"端口名重复", Fleet{Devices: []Device{{Name: "a", SNMP: AgentProfile{MaxRepetitions: 8},
			Interfaces: []Interface{{IfIndex: 1, IfName: "x"}, {IfIndex: 2, IfName: "x"}}}}}},
		{"能力上限为 0", Fleet{Devices: []Device{{Name: "a", SNMP: AgentProfile{MaxRepetitions: 0},
			Interfaces: []Interface{{IfIndex: 1, IfName: "x"}}}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.f.Validate(); err == nil {
				t.Fatalf("%s 应该报错", c.name)
			}
		})
	}
}

func TestLoadFleetMissingFile(t *testing.T) {
	if _, err := LoadFleet(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("文件不存在应该报错")
	}
}

func TestLoadFleetBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFleet(path); err == nil {
		t.Fatal("坏 JSON 应该报错")
	}
}

// Entries 是 agent 应答的数据源：必须按 OID 字典序、不能有重复叶子。
func TestDeviceEntriesSortedAndUnique(t *testing.T) {
	f, err := LoadFleet("samples/fleet.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Devices {
		es := d.Entries()
		want := 4 + len(Columns)*len(d.Interfaces) + 5 // system 组 4 条、12 列、表尾 5 条
		if len(es) != want {
			t.Fatalf("%s 的叶子数 = %d，期望 %d", d.Name, len(es), want)
		}
		for i := 1; i < len(es); i++ {
			if cmp := es[i-1].OID.Compare(es[i].OID); cmp >= 0 {
				t.Fatalf("%s 的叶子没有严格升序: %s 之后是 %s", d.Name, es[i-1].OID, es[i].OID)
			}
		}
	}
}

// 12 列的含义：ifIndex 是行索引，不是走出来的列。
func TestColumnsCoverBothTables(t *testing.T) {
	if len(Columns) != 12 {
		t.Fatalf("列数 = %d，期望 12", len(Columns))
	}
	perTable := map[string]int{}
	kinds := map[string]ValueKind{}
	for _, c := range Columns {
		perTable[c.Table]++
		kinds[c.Name] = c.Kind
		if !c.OID.HasPrefix(MustOID(ifTableBase)) && !c.OID.HasPrefix(MustOID(ifXTableBase)) {
			t.Fatalf("列 %s 的 OID %s 不落在两张表里", c.Name, c.OID)
		}
	}
	if perTable["ifTable"] != 7 || perTable["ifXTable"] != 5 {
		t.Fatalf("列分布 = %v，期望 ifTable 7 / ifXTable 5", perTable)
	}
	if kinds["ifHCInOctets"] != KindCounter64 {
		t.Fatalf("ifHCInOctets 必须是 counter64，32 位装不下 1G 口一分钟的字节数")
	}
	if kinds["ifInErrors"] != KindCounter32 {
		t.Fatalf("ifInErrors 是 counter32")
	}
	if kinds["ifAlias"] != KindString {
		t.Fatalf("ifAlias 是字符串")
	}
	if _, ok := ColumnByName("ifIndex"); ok {
		t.Fatal("ifIndex 不是可遍历的列，它是行索引")
	}
}

// ifSpeed 是 32 位、单位 bit/s：100G 口读出来是顶格值，速率必须看 ifHighSpeed。
func TestIfSpeedSaturatesAt32Bit(t *testing.T) {
	f, err := LoadFleet("samples/fleet.json")
	if err != nil {
		t.Fatal(err)
	}
	d, ok := f.DeviceByName("spine-01")
	if !ok {
		t.Fatal("样本里没有 spine-01")
	}
	itf := d.Interfaces[0]
	if itf.IfSpeedBps != 4294967295 {
		t.Fatalf("100G 口的 ifSpeed 应为 4294967295，实际 %d", itf.IfSpeedBps)
	}
	if itf.IfHighSpeedMbps != 100000 {
		t.Fatalf("ifHighSpeed 应为 100000（Mbps），实际 %d", itf.IfHighSpeedMbps)
	}
	// 1G 口不溢出，两个字段能对上。
	legacy, _ := f.DeviceByName("leaf-legacy-01")
	if legacy.Interfaces[0].IfSpeedBps != 1_000_000_000 {
		t.Fatalf("1G 口的 ifSpeed 应为 1e9，实际 %d", legacy.Interfaces[0].IfSpeedBps)
	}
}
