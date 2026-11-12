package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestApplySampleConverges(t *testing.T) {
	ledger, lldp, arp := loadSamples(t)
	r, err := Reconcile(ledger, lldp, arp)
	if err != nil {
		t.Fatal(err)
	}

	applied, err := Apply(ledger, r.Patches)
	if err != nil {
		t.Fatal(err)
	}

	// 补丁对不对，唯一硬的验收标准：把补丁应用一遍，再对一次账，应当零差异。
	// 不收敛说明补丁写的不是目标状态，或者差异单漏了条目。
	again, err := Reconcile(applied, lldp, arp)
	if err != nil {
		t.Fatal(err)
	}
	for name, diffs := range map[string][]Diff{
		"僵尸":   again.Zombies,
		"幽灵":   again.Ghosts,
		"错挂":   again.Mismatches,
		"改名":   again.Renames,
		"端口重排": again.Reindexes,
	} {
		if len(diffs) != 0 {
			t.Errorf("补丁应用之后 %s 还剩 %d 条: %+v", name, len(diffs), diffs)
		}
	}

	// 旧名那条记录还在，只是标了 retired 与 alias_of——软删，不删记录。
	old, err := deviceOf(applied, "tor-3f-02")
	if err != nil {
		t.Fatal(err)
	}
	if old.Lifecycle != LifecycleRetired || old.AliasOf != "tor-a3f-02" {
		t.Errorf("旧名应当是 retired + alias_of，实际 %+v", old)
	}

	// 幽灵设备登记成 staging：还没验收，不能直接进 active 的采集范围。
	ghost, err := deviceOf(applied, "tor-a3f-04")
	if err != nil {
		t.Fatal(err)
	}
	if ghost.Lifecycle != LifecycleStaging || ghost.MgmtIP != "10.20.3.14" {
		t.Errorf("幽灵应当以 staging 登记并带上 LLDP 里的管理地址，实际 %+v", ghost)
	}
}

func TestApplyDoesNotMutateInput(t *testing.T) {
	ledger, lldp, arp := loadSamples(t)
	r, err := Reconcile(ledger, lldp, arp)
	if err != nil {
		t.Fatal(err)
	}
	before := len(ledger.Devices)
	targets := map[string]string{}
	for _, d := range ledger.Devices {
		targets[d.Hostname] = d.Lifecycle
	}

	if _, err := Apply(ledger, r.Patches); err != nil {
		t.Fatal(err)
	}

	if len(ledger.Devices) != before {
		t.Errorf("Apply 改动了入参台账：设备数 %d -> %d", before, len(ledger.Devices))
	}
	for _, d := range ledger.Devices {
		if targets[d.Hostname] != d.Lifecycle {
			t.Errorf("Apply 改动了入参台账：%s 的 lifecycle %s -> %s",
				d.Hostname, targets[d.Hostname], d.Lifecycle)
		}
	}
}

func TestApplyRejectsUnknownOp(t *testing.T) {
	ledger, _, _ := loadSamples(t)
	_, err := Apply(ledger, []Patch{{ID: "P99", Op: "device_rewrite_everything", Target: "spine-01"}})
	if err == nil {
		t.Error("不认识的 op 应当报错，不能当成没事")
	}
}

func TestApplyRejectsUnknownField(t *testing.T) {
	ledger, _, _ := loadSamples(t)
	_, err := Apply(ledger, []Patch{{
		ID: "P99", Op: OpDeviceLifecycle, Target: "spine-01",
		Set: map[string]string{"lifecycle": LifecycleRetired, "owner": "别人"},
	}})
	if err == nil {
		t.Error("白名单以外的字段应当报错：静默忽略比报错危险得多")
	}
}

func TestApplyRejectsDuplicateDeviceCreate(t *testing.T) {
	ledger, _, _ := loadSamples(t)
	_, err := Apply(ledger, []Patch{{
		ID: "P99", Op: OpDeviceCreate, Target: "spine-01",
		Set: map[string]string{"hostname": "spine-01"},
	}})
	if err == nil {
		t.Error("已登记的设备不能重复创建")
	}
}

func TestApplyRejectsAliasToMissingDevice(t *testing.T) {
	ledger, _, _ := loadSamples(t)
	_, err := Apply(ledger, []Patch{{
		ID: "P99", Op: OpDeviceAlias, Target: "tor-3f-02",
		Set: map[string]string{"alias_of": "tor-不存在"},
	}})
	if err == nil {
		t.Error("alias_of 指向不存在的设备应当报错")
	}
}

func TestApplyRejectsLocalPortCollision(t *testing.T) {
	ledger, _, _ := loadSamples(t)
	_, err := Apply(ledger, []Patch{{
		ID: "P99", Op: OpLinkLocalPort, Target: "tor-a3f-01#Ethernet1/1",
		Set: map[string]string{"port": "Ethernet1/2"},
	}})
	if err == nil {
		t.Error("改本端端口名撞上已有声明应当报错")
	}
}

func TestGuardWritePathRejectsLedger(t *testing.T) {
	if err := guardWritePath("samples/cmdb.json", "samples/cmdb.json"); err == nil {
		t.Error("写库路径等于台账路径时必须拒绝")
	}
	if err := guardWritePath("samples/cmdb.json", "samples/../samples/cmdb.json"); err == nil {
		t.Error("绕一圈还是台账本身，也要拒绝")
	}
	if err := guardWritePath("samples/cmdb.json", "out/cmdb.applied.json"); err != nil {
		t.Errorf("副本路径应当放行: %v", err)
	}
}

// dry-run 一个字节都不许写台账。
func TestRunDryRunLeavesLedgerUntouched(t *testing.T) {
	before := readFile(t, "samples/cmdb.json")
	outDir := filepath.Join(t.TempDir(), "out")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-out", outDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("dry-run 应当正常退出，实际 %d：%s", code, stderr.String())
	}
	if after := readFile(t, "samples/cmdb.json"); !bytes.Equal(before, after) {
		t.Fatal("dry-run 之后台账字节变了")
	}

	for _, name := range []string{"diffs.jsonl", "cmdb-patch.jsonl"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("dry-run 应当产出 %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "cmdb.applied.json")); err == nil {
		t.Error("dry-run 不该产出台账副本")
	}
	if !bytes.Contains(stdout.Bytes(), []byte("未被改写")) {
		t.Errorf("输出里应当明确说出台账没变: %s", stdout.String())
	}
}

func TestRunApplyWithoutConfirmRefused(t *testing.T) {
	before := readFile(t, "samples/cmdb.json")
	writePath := filepath.Join(t.TempDir(), "applied.json")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-out", "", "-apply", "-write", writePath}, &stdout, &stderr); code != 2 {
		t.Fatalf("-apply 不带 -confirm 应当以 2 退出，实际 %d", code)
	}
	if _, err := os.Stat(writePath); err == nil {
		t.Error("没确认就写了副本")
	}
	if after := readFile(t, "samples/cmdb.json"); !bytes.Equal(before, after) {
		t.Fatal("台账被改了")
	}
}

func TestRunApplyWritesCopyOnly(t *testing.T) {
	before := readFile(t, "samples/cmdb.json")
	writePath := filepath.Join(t.TempDir(), "applied.json")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-out", "", "-apply", "-confirm", "-write", writePath}, &stdout, &stderr); code != 0 {
		t.Fatalf("确认之后应当写入副本，实际退出码 %d：%s", code, stderr.String())
	}
	if after := readFile(t, "samples/cmdb.json"); !bytes.Equal(before, after) {
		t.Fatal("落库之后台账本身仍然不该变")
	}
	if _, err := os.Stat(writePath); err != nil {
		t.Fatalf("副本没写出来: %v", err)
	}

	copied, err := loadLedger(writePath)
	if err != nil {
		t.Fatalf("副本读不回来，说明写出的不是合法台账: %v", err)
	}
	_, lldp, arp := loadSamples(t)
	again, err := Reconcile(copied, lldp, arp)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.All()) != 0 {
		t.Errorf("副本上应当零差异，实际还剩 %d 条", len(again.All()))
	}
}

func TestRunApplyRefusesLedgerAsWriteTarget(t *testing.T) {
	before := readFile(t, "samples/cmdb.json")

	var stdout, stderr bytes.Buffer
	code := run([]string{"-out", "", "-apply", "-confirm", "-write", "samples/cmdb.json"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("把台账本身当写库目标必须失败")
	}
	if after := readFile(t, "samples/cmdb.json"); !bytes.Equal(before, after) {
		t.Fatal("被拒绝的写库请求居然改了台账")
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
