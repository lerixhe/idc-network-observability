package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDevPortString(t *testing.T) {
	k := devPort{device: "spine-01", port: "Ethernet1/5"}
	if got := k.String(); got != "spine-01#Ethernet1/5" {
		t.Errorf("String() = %q", got)
	}
	back, err := splitTarget(k.String())
	if err != nil {
		t.Fatal(err)
	}
	if back != k {
		t.Errorf("splitTarget 还原不回来: %+v", back)
	}
}

func TestSplitTargetRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "spine-01", "#Ethernet1/5", "spine-01#"} {
		if _, err := splitTarget(s); err == nil {
			t.Errorf("%q 应当报错，不能猜出个端口来", s)
		}
	}
}

func TestValidateRejectsDuplicateHostname(t *testing.T) {
	l := &Ledger{Devices: []Device{
		{Hostname: "sw-a", Lifecycle: LifecycleActive},
		{Hostname: "sw-a", Lifecycle: LifecycleActive},
	}}
	if err := l.Validate(); err == nil {
		t.Error("同一个 hostname 两条记录应当报错")
	}
}

func TestValidateRejectsBadLifecycle(t *testing.T) {
	l := &Ledger{Devices: []Device{{Hostname: "sw-a", Lifecycle: "online"}}}
	if err := l.Validate(); err == nil {
		t.Error("lifecycle 只认 active/staging/retired，别的值应当报错")
	}
}

func TestValidateRejectsDuplicatePortDecl(t *testing.T) {
	l := &Ledger{
		Devices: []Device{{Hostname: "sw-a", Lifecycle: LifecycleActive}},
		Links: []PortDecl{
			{Device: "sw-a", Port: "Ethernet1/1", PeerDevice: "sw-b", PeerPort: "Ethernet1/1"},
			{Device: "sw-a", Port: "Ethernet1/1", PeerDevice: "sw-b", PeerPort: "Ethernet1/2"},
		},
	}
	if err := l.Validate(); err == nil {
		t.Error("一个端口两条声明应当报错")
	}
}

func TestLoadSamplesShape(t *testing.T) {
	ledger, err := loadLedger("samples/cmdb.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Devices) != 10 {
		t.Errorf("台账应当 10 条设备记录，实际 %d", len(ledger.Devices))
	}
	if len(ledger.Links) != 13 {
		t.Errorf("台账应当 13 条端口声明，实际 %d", len(ledger.Links))
	}

	lldp, err := loadLLDP("samples/lldp.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(lldp.Ports) != 15 {
		t.Errorf("LLDP 样本应当 15 个端口，实际 %d", len(lldp.Ports))
	}

	arp, err := loadARP("samples/arp.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(arp.Entries) != 8 {
		t.Errorf("ARP 样本应当 8 条，实际 %d", len(arp.Entries))
	}
}

func TestLoadLLDPRejectsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lldp.json")
	if err := os.WriteFile(path, []byte(`{"collected_at":"","ports":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLLDP(path); err == nil {
		t.Error("空 LLDP 快照应当报错：后续每条判据都在对着空表说话")
	}
}

func TestLoadARPAllowsEmptyTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arp.json")
	if err := os.WriteFile(path, []byte(`{"collected_at":"","gateway":"gw","entries":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadARP(path); err != nil {
		t.Errorf("空 ARP 表是合法状态，不该报错: %v", err)
	}
}
