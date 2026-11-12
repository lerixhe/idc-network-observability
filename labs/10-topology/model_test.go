package main

import (
	"os"
	"path/filepath"
	"testing"
)

func mustDirectory(t *testing.T) *Directory {
	t.Helper()
	dir, err := loadDirectory(filepath.Join("samples", "cmdb.json"))
	if err != nil {
		t.Fatalf("读样本台账失败: %v", err)
	}
	return dir
}

func newDirectory(t *testing.T, c CMDB) *Directory {
	t.Helper()
	dir, err := NewDirectory(c)
	if err != nil {
		t.Fatalf("建归一化器失败: %v", err)
	}
	return dir
}

func TestDirectoryDeviceAliases(t *testing.T) {
	dir := mustDirectory(t)
	cases := []struct{ raw, want string }{
		{"spine-01", "spine-01"},
		{"SPINE-01", "spine-01"},
		{"spine-01.room1.example.net", "spine-01"},
		{"10.20.3.11", "tor-a3f-01"},
		{"tor-a3f-01.room3.example.net", "tor-a3f-01"},
		{" TOR-A3F-04 ", "tor-a3f-04"},
	}
	for _, c := range cases {
		got, ok := dir.Device(c.raw)
		if !ok || got != c.want {
			t.Errorf("Device(%q) = %q, %v；期望 %q", c.raw, got, ok, c.want)
		}
	}
	if got, ok := dir.Device("acc-a3f-09"); ok {
		t.Errorf("台账里没有 acc-a3f-09，却归一化成了 %q", got)
	}
	if _, ok := dir.Device(""); ok {
		t.Error("空串不该归一化成功")
	}
}

func TestDirectoryPortAliases(t *testing.T) {
	dir := mustDirectory(t)
	// 同一个端口的四种写法：规范名、两种厂商别名、ifIndex。
	for _, raw := range []string{"Ethernet1/48", "Eth1/48", "Gi1/0/48", "48"} {
		if got, ok := dir.Port("spine-01", raw); !ok || got != "Ethernet1/48" {
			t.Errorf("Port(spine-01, %q) = %q, %v；期望 Ethernet1/48", raw, got, ok)
		}
	}
	// 大小写不敏感：probe 源常把端口名转小写。
	if got, ok := dir.Port("tor-a3f-03", "ethernet1/7"); !ok || got != "Ethernet1/7" {
		t.Errorf("Port(tor-a3f-03, ethernet1/7) = %q, %v", got, ok)
	}
	// 别名表里没有就是没有，不许猜。
	if got, ok := dir.Port("tor-a3f-02", "Gi1/0/99"); ok {
		t.Errorf("Gi1/0/99 不在 tor-a3f-02 的端口表里，却返回了 %q", got)
	}
	// ifIndex 是按设备隔离的：spine-01 的 48 口不等于 tor-a3f-02 的 48 口。
	if _, ok := dir.Port("tor-a3f-02", "48"); ok {
		t.Error("tor-a3f-02 没有 ifIndex 48 的端口")
	}
}

func TestDirectoryFoldLag(t *testing.T) {
	dir := mustDirectory(t)
	cases := []struct{ device, port, want string }{
		{"spine-01", "Ethernet1/10", "Port-Channel10"}, // 成员口折到逻辑口
		{"spine-01", "Ethernet1/11", "Port-Channel10"},
		{"spine-01", "Port-Channel10", "Port-Channel10"}, // 逻辑口自己不动
		{"tor-a3f-01", "Ethernet1/10", "Eth-Trunk1"},
		{"spine-01", "Ethernet1/1", "Ethernet1/1"}, // 独立口原样
	}
	for _, c := range cases {
		if got := dir.fold(c.device, c.port); got.Port != c.want {
			t.Errorf("fold(%s, %s) = %s；期望 %s", c.device, c.port, got.Port, c.want)
		}
	}
	if !dir.IsLag("spine-01", "Port-Channel10") {
		t.Error("Port-Channel10 应当是逻辑口")
	}
	if dir.IsLag("spine-01", "Ethernet1/10") {
		t.Error("Ethernet1/10 是成员口，不是逻辑口")
	}
	if got := dir.Members("tor-a3f-01", "Eth-Trunk1"); len(got) != 2 {
		t.Errorf("Eth-Trunk1 应当有两个成员，实际 %v", got)
	}
}

func TestDirectoryMACAndRouterID(t *testing.T) {
	dir := mustDirectory(t)
	if got, ok := dir.DeviceByMAC("00:1B:21:AA:00:11"); !ok || got != "spine-01" {
		t.Errorf("DeviceByMAC = %q, %v；期望 spine-01（MAC 大小写不敏感）", got, ok)
	}
	if _, ok := dir.DeviceByMAC("00:1b:21:aa:00:99"); ok {
		t.Error("未知 MAC 不该反查出设备")
	}
	if got, ok := dir.DeviceByRouterID("10.20.3.12"); !ok || got != "tor-a3f-02" {
		t.Errorf("DeviceByRouterID = %q, %v", got, ok)
	}
}

func TestNewDirectoryRejectsDirtyCMDB(t *testing.T) {
	base := CMDB{
		Devices: []Device{{
			Name:  "sw-1",
			Ports: []CMDBPort{{Name: "Ethernet1/1", IfIndex: 1}},
		}},
	}
	if _, err := NewDirectory(base); err != nil {
		t.Fatalf("干净台账不该报错: %v", err)
	}

	dupDevice := CMDB{Devices: []Device{{Name: "sw-1"}, {Name: "sw-1"}}}
	if _, err := NewDirectory(dupDevice); err == nil {
		t.Error("重复登记的设备应当报错")
	}

	aliasClash := CMDB{Devices: []Device{
		{Name: "sw-1", Aliases: []string{"tor-1"}},
		{Name: "sw-2", Aliases: []string{"TOR-1"}},
	}}
	if _, err := NewDirectory(aliasClash); err == nil {
		t.Error("同一别名指向两台设备应当报错（大小写不敏感）")
	}

	indexClash := CMDB{Devices: []Device{{
		Name: "sw-1",
		Ports: []CMDBPort{
			{Name: "Ethernet1/1", IfIndex: 1},
			{Name: "Ethernet1/2", IfIndex: 1},
		},
	}}}
	if _, err := NewDirectory(indexClash); err == nil {
		t.Error("同一个 ifIndex 挂在两个端口上应当报错")
	}

	missingMember := CMDB{
		Devices: []Device{{Name: "sw-1", Ports: []CMDBPort{{Name: "Ethernet1/1"}}}},
		Lags:    []Lag{{Name: "Eth-Trunk1", Device: "sw-1", Members: []string{"Ethernet1/9"}}},
	}
	if _, err := NewDirectory(missingMember); err == nil {
		t.Error("聚合口的成员不在端口表里应当报错")
	}
}

func TestLoadSamples(t *testing.T) {
	lldp, err := loadLLDP(filepath.Join("samples", "lldp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(lldp) != 17 {
		t.Errorf("LLDP 样本应当有 17 行，实际 %d", len(lldp))
	}

	stp, err := loadSTP(filepath.Join("samples", "stp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stp) != 4 {
		t.Errorf("STP 样本应当有 4 行，实际 %d", len(stp))
	}

	routes, err := loadRoutes(filepath.Join("samples", "route.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 3 {
		t.Errorf("路由邻居样本应当有 3 行，实际 %d", len(routes))
	}

	macs, err := loadMACs(filepath.Join("samples", "mac.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(macs) != 4 {
		t.Errorf("MAC 样本应当有 4 行，实际 %d", len(macs))
	}
}

func TestLoadEventStreamCountsOutOfOrder(t *testing.T) {
	stream, err := loadEventStream(filepath.Join("samples", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stream.Raw) != 19 {
		t.Fatalf("事件样本应当有 19 条，实际 %d", len(stream.Raw))
	}
	// 样本里故意把 10:14:02 那条放在 10:14:05 之后，还有 10:19、10:20、10:13:5x
	// 三批迟到条目。
	if stream.OutOfOrder != 6 {
		t.Errorf("乱序条数 = %d，期望 6", stream.OutOfOrder)
	}

	burst, err := loadEventStream(filepath.Join("samples", "events-burst.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(burst.Raw) != 192 {
		t.Fatalf("压力样本应当有 192 条，实际 %d", len(burst.Raw))
	}
	if burst.OutOfOrder != 0 {
		t.Errorf("压力样本按时间顺序录制，乱序应为 0，实际 %d", burst.OutOfOrder)
	}
}

func TestLoadEventsRejectsBadSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	content := `{"ts":"2026-05-12T10:00:00Z","source":"netconf","device":"sw-1","port":"1","kind":"x"}` + "\n"
	if err := writeFile(path, content); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEventStream(path); err == nil {
		t.Error("不认识的 source 应当报错")
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
