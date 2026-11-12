package main

import (
	"testing"
	"time"
)

func loadAgent(t *testing.T, name string) *Agent {
	t.Helper()
	f, err := LoadFleet("samples/fleet.json")
	if err != nil {
		t.Fatal(err)
	}
	d, ok := f.DeviceByName(name)
	if !ok {
		t.Fatalf("样本里没有 %s", name)
	}
	return NewAgent(d, 1)
}

// GetBulk 是 GetNext 的批量版：从 start 之后开始，永远不返回 start 自己。
func TestGetBulkStartsStrictlyAfter(t *testing.T) {
	a := loadAgent(t, "spine-01")
	col, _ := ColumnByName("ifName")
	start := col.OID
	vbs := a.GetBulk(start, 3)
	if len(vbs) != 3 {
		t.Fatalf("要 3 条拿到 %d 条", len(vbs))
	}
	if vbs[0].OID.String() != col.OID.Child(1).String() {
		t.Fatalf("第一条应是 %s，实际 %s", col.OID.Child(1), vbs[0].OID)
	}
	// 接着上一批的最后一条往下取，下一个必须是 index 4。
	next := a.GetBulk(vbs[2].OID, 1)
	if next[0].OID.Last() != 4 {
		t.Fatalf("从 index 3 之后应取到 4，实际 %d", next[0].OID.Last())
	}
}

func TestGetBulkCapsAtDeviceCapability(t *testing.T) {
	a := loadAgent(t, "tor-a3f-02") // C 档，能力上限 8
	col, _ := ColumnByName("ifName")
	resp := a.Simulate(BulkRequest{Start: col.OID, MaxRepetitions: 64}, time.Second)
	if resp.Error != ErrTooBig {
		t.Fatalf("要 64 条应回 tooBig，实际 %q", resp.Error)
	}
	if len(resp.Varbinds) != 0 {
		t.Fatalf("tooBig 不该带回 varbind，实际 %d 条", len(resp.Varbinds))
	}
	if ok := a.Simulate(BulkRequest{Start: col.OID, MaxRepetitions: 8}, time.Second); ok.Error != "" {
		t.Fatalf("8 条是设备上限，不该报错: %q", ok.Error)
	}
}

func TestSimulateTimeoutAndUnreachable(t *testing.T) {
	// leaf-legacy-01：1.6s ± 0.3s，超时给 1s 必挂。
	slow := loadAgent(t, "leaf-legacy-01")
	col, _ := ColumnByName("ifName")
	resp := slow.Simulate(BulkRequest{Start: col.OID, MaxRepetitions: 8}, time.Second)
	if resp.Error != ErrTimeout {
		t.Fatalf("1s 超时应该挂，实际 %q", resp.Error)
	}
	if resp.ElapsedMs != 1000 {
		t.Fatalf("超时的耗时按超时算，期望 1000ms，实际 %d", resp.ElapsedMs)
	}

	// tor-b7f-01：整台不可达，给多长超时都没用。
	dead := loadAgent(t, "tor-b7f-01")
	resp = dead.Simulate(BulkRequest{Start: col.OID, MaxRepetitions: 16}, 5*time.Second)
	if resp.Error != ErrTimeout {
		t.Fatalf("不可达设备应回 timeout，实际 %q", resp.Error)
	}
}

// 走得比 MIB 末尾还远时要给 endOfMibView，而不是少几条让你以为丢了。
func TestGetBulkEndOfMibView(t *testing.T) {
	a := loadAgent(t, "spine-01")
	last := a.Entries[len(a.Entries)-1].OID
	vbs := a.GetBulk(last, 4)
	if len(vbs) != 1 || !vbs[0].EndOfMibView {
		t.Fatalf("末尾之后应给一条 endOfMibView，实际 %+v", vbs)
	}
}

// 同一个种子同一份样本，抖动必须一模一样，否则采集账对不上。
func TestAgentDeterministic(t *testing.T) {
	col, _ := ColumnByName("ifName")
	req := BulkRequest{Start: col.OID, MaxRepetitions: 8}
	a1 := loadAgent(t, "tor-a3f-02")
	a2 := loadAgent(t, "tor-a3f-02")
	for i := 0; i < 8; i++ {
		r1 := a1.Simulate(req, 5*time.Second)
		r2 := a2.Simulate(req, 5*time.Second)
		if r1.ElapsedMs != r2.ElapsedMs {
			t.Fatalf("第 %d 次往返耗时不同：%d vs %d", i, r1.ElapsedMs, r2.ElapsedMs)
		}
	}
}

// 表尾之后还有别的 OID：agent 不会自己停，停是采集器的事。
func TestTableEndStillHasForeignOIDs(t *testing.T) {
	a := loadAgent(t, "tor-a3f-01")
	ifxLast, _ := ColumnByName("ifAlias")
	start := ifxLast.OID.Child(24) // 最后一个端口的 ifAlias
	vbs := a.GetBulk(start, 3)
	if len(vbs) == 0 {
		t.Fatal("ifAlias 之后应该还有 OID（ifStackLastChange）")
	}
	if vbs[0].OID.HasPrefix(ifxLast.OID) {
		t.Fatalf("最后一行的 ifAlias 之后不该还有 ifAlias: %s", vbs[0].OID)
	}
}
