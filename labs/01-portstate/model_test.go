package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperName(t *testing.T) {
	cases := []struct {
		value int
		want  string
	}{
		{operUp, "up"},
		{operDown, "down"},
		{operTesting, "testing"},
		{operUnknown, "unknown"},
		{operDormant, "dormant"},
		{operNotPresent, "notPresent"},
		{operLowerLayerDown, "lowerLayerDown"},
		{0, "oper(0)"},
		{42, "oper(42)"},
	}
	for _, c := range cases {
		if got := operName(c.value); got != c.want {
			t.Errorf("operName(%d) = %q, 期望 %q", c.value, got, c.want)
		}
	}
}

func TestAdminName(t *testing.T) {
	if got := adminName(adminUp); got != "up" {
		t.Errorf("adminName(1) = %q", got)
	}
	if got := adminName(adminDown); got != "down" {
		t.Errorf("adminName(2) = %q", got)
	}
}

func TestLoadPortsFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	content := `[
	  {"device":"sw-a","if_name":"Ethernet1/1","if_index":1,"if_admin_status":1,"if_oper_status":2,
	   "if_speed_mbps":0,"expect_mbps":10000,"link_id":"L1",
	   "optic":{"present":true,"model":"SFP-10G-SR","rx_power_dbm":null,"tx_power_dbm":-2.0,"rx_low_alarm_dbm":-14.0}}
	]`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ports, err := loadPorts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 {
		t.Fatalf("应当解析出 1 个端口，实际 %d", len(ports))
	}
	p := ports[0]
	if p.Device != "sw-a" || p.IfName != "Ethernet1/1" || p.IfIndex != 1 {
		t.Errorf("端口身份解析不对: %+v", p)
	}
	if p.Optic == nil {
		t.Fatal("optic 应当解析出来")
	}
	if p.Optic.RxPowerDbm != nil {
		t.Errorf("rx_power_dbm 是 null，应当解析成 nil，实际 %v", *p.Optic.RxPowerDbm)
	}
	if p.Optic.TxPowerDbm == nil || *p.Optic.TxPowerDbm != -2.0 {
		t.Errorf("tx_power_dbm 解析不对: %+v", p.Optic)
	}
}

func TestLoadPortsRejectsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPorts(path); err == nil {
		t.Error("空快照应当报错，否则后面每个判据都在对着空表说话")
	}
}

func TestLoadTrapsParsesTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traps.jsonl")
	content := strings.Join([]string{
		`{"ts":"2026-05-12T10:00:05Z","device":"sw-a","if_index":9,"kind":"link_down","sys_uptime_ms":3600000}`,
		``,
		`{"ts":"2026-05-12T10:00:48Z","device":"sw-a","if_index":9,"kind":"link_up","sys_uptime_ms":3643000}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	traps, err := loadTraps(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(traps) != 2 {
		t.Fatalf("空行应当跳过，期望 2 条，实际 %d", len(traps))
	}
	if traps[0].unixMs >= traps[1].unixMs {
		t.Errorf("时间戳没解成递增的毫秒: %d / %d", traps[0].unixMs, traps[1].unixMs)
	}
	gap := traps[1].unixMs - traps[0].unixMs
	if gap != 43_000 {
		t.Errorf("两条 Trap 间隔应当是 43000ms，实际 %d", gap)
	}
}

func TestLoadTrapsRejectsBadKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traps.jsonl")
	content := `{"ts":"2026-05-12T10:00:05Z","device":"sw-a","if_index":9,"kind":"link_flap","sys_uptime_ms":100}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTraps(path); err == nil {
		t.Error("不认识的 kind 应当报错，不能静默当成 link_up")
	}
}

func TestLoadTrapsRejectsBadTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traps.jsonl")
	content := `{"ts":"2026-05-12 10:00:05","device":"sw-a","if_index":9,"kind":"link_down","sys_uptime_ms":100}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTraps(path); err == nil {
		t.Error("非 RFC3339 时间戳应当报错")
	}
}

func TestSamplesLoadable(t *testing.T) {
	ports, err := loadPorts("samples/portstate.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 20 {
		t.Errorf("样本应当是 20 个端口，实际 %d", len(ports))
	}
	traps, err := loadTraps("samples/traps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(traps) != 19 {
		t.Errorf("样本应当是 19 条 Trap，实际 %d", len(traps))
	}
}
