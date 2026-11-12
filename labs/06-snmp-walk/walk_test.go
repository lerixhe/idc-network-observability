package main

import (
	"errors"
	"strings"
	"testing"
)

// fakeRequest 按给定 agent 应答，只记账不算时间，专门测 walk 的取数逻辑。
func fakeRequest(t *testing.T, a *Agent, stats *WalkStats) Requester {
	t.Helper()
	return func(req BulkRequest) (Response, error) {
		stats.Requests++
		if req.MaxRepetitions > a.Profile.MaxRepetitions {
			return Response{Error: ErrTooBig}, nil
		}
		return Response{Varbinds: a.GetBulk(req.Start, req.MaxRepetitions)}, nil
	}
}

func TestWalkColumnCoversAllRows(t *testing.T) {
	col, _ := ColumnByName("ifName")
	a := loadAgent(t, "tor-a3f-02") // 24 个端口，能力上限 8
	var st WalkStats
	vbs, err := WalkColumn(col, 8, fakeRequest(t, a, &st), &st)
	if err != nil {
		t.Fatalf("遍历报错: %v", err)
	}
	if len(vbs) != 24 {
		t.Fatalf("拿到 %d 条，期望 24", len(vbs))
	}
	if st.PDUs != 4 {
		t.Fatalf("24 行 / 8 条 = 3 页加 1 页表尾，实际 %d 页", st.PDUs)
	}
	for i, vb := range vbs {
		if int(vb.OID.Last()) != i+1 {
			t.Fatalf("第 %d 条的行索引 = %d", i, vb.OID.Last())
		}
		if vb.Value.Str == "" {
			t.Fatalf("行 %d 的 ifName 是空的", vb.OID.Last())
		}
	}
}

// 表尾那一条必须被丢掉：ifTable 之后紧跟着 ip 组，ifXTable 之后还有 ifStackLastChange。
func TestWalkColumnStopsAtSubtreeEnd(t *testing.T) {
	col, _ := ColumnByName("ifAlias")
	a := loadAgent(t, "tor-a3f-01")
	var st WalkStats
	vbs, err := WalkColumn(col, 16, fakeRequest(t, a, &st), &st)
	if err != nil {
		t.Fatalf("遍历报错: %v", err)
	}
	if len(vbs) != 24 {
		t.Fatalf("ifAlias 有 24 行，拿到 %d 条（说明走出了子树）", len(vbs))
	}
	for _, vb := range vbs {
		if !vb.OID.HasPrefix(col.OID) {
			t.Fatalf("混进了不属于 ifAlias 的 OID: %s", vb.OID)
		}
	}
}

// 设备回 tooBig 就砍半重来，而不是放弃这一列。
func TestWalkColumnBacksOffOnTooBig(t *testing.T) {
	col, _ := ColumnByName("ifName")
	a := loadAgent(t, "tor-a3f-02") // 能力上限 8
	var st WalkStats
	vbs, err := WalkColumn(col, 64, fakeRequest(t, a, &st), &st)
	if err != nil {
		t.Fatalf("背退之后应该能采完: %v", err)
	}
	if len(vbs) != 24 {
		t.Fatalf("背退之后拿到 %d 条，期望 24", len(vbs))
	}
	if st.TooBig != 3 {
		t.Fatalf("64→32→16→8 应该被拒 3 次，实际 %d", st.TooBig)
	}
	if st.Reps != 8 {
		t.Fatalf("最终应落到设备上限 8，实际 %d", st.Reps)
	}
}

// 1 条/请求还是 tooBig 就说明这列采不了，必须报错而不是死循环。
func TestWalkColumnTooBigAtOne(t *testing.T) {
	col, _ := ColumnByName("ifName")
	a := loadAgent(t, "tor-a3f-02")
	a.Profile.MaxRepetitions = 0 // 每请求都拒
	var st WalkStats
	_, err := WalkColumn(col, 4, fakeRequest(t, a, &st), &st)
	if err == nil {
		t.Fatal("始终 tooBig 应该报错")
	}
	if !strings.Contains(err.Error(), "tooBig") {
		t.Fatalf("错误里应说明 tooBig: %v", err)
	}
}

// 请求层报错（超时）要原样抛上来，交给采集器决定重试还是放弃。
func TestWalkColumnPropagatesRequestError(t *testing.T) {
	col, _ := ColumnByName("ifName")
	var st WalkStats
	boom := errors.New("设备不通")
	_, err := WalkColumn(col, 8, func(BulkRequest) (Response, error) {
		st.Requests++
		return Response{}, boom
	}, &st)
	if !errors.Is(err, boom) {
		t.Fatalf("应把原始错误抛上来，实际 %v", err)
	}
}

// 应答不回进度（一直给同一个 OID）时必须停，不能无限要。
func TestWalkColumnStopsOnNoProgress(t *testing.T) {
	col, _ := ColumnByName("ifName")
	var st WalkStats
	stuck := func(BulkRequest) (Response, error) {
		st.Requests++
		return Response{Varbinds: []Varbind{{OID: col.OID.Child(1), Value: StringValue("x")}}}, nil
	}
	vbs, err := WalkColumn(col, 8, stuck, &st)
	if err != nil {
		t.Fatalf("报错: %v", err)
	}
	if len(vbs) != 1 {
		t.Fatalf("只该拿到 1 条，实际 %d", len(vbs))
	}
	if st.PDUs != 2 {
		t.Fatalf("第一页拿到 1 条，第二页没进度就该停，实际 %d 页", st.PDUs)
	}
}

// 一列 12 次遍历拼成行：24 个端口就是 24 行，每行 12 个字段。
func TestAssembleRows(t *testing.T) {
	a := loadAgent(t, "tor-a3f-01")
	rows := RowSet{}
	for _, col := range Columns {
		var st WalkStats
		vbs, err := WalkColumn(col, 16, fakeRequest(t, a, &st), &st)
		if err != nil {
			t.Fatalf("列 %s 遍历失败: %v", col.Name, err)
		}
		rows.Add(col, vbs)
	}
	sorted := rows.Sorted()
	if len(sorted) != 24 {
		t.Fatalf("拼出 %d 行，期望 24", len(sorted))
	}
	first := sorted[0]
	if first.Index != 1 {
		t.Fatalf("第一行的 ifIndex = %d", first.Index)
	}
	if v, _ := first.Get("ifName"); v.Str != "Ethernet1/1" {
		t.Fatalf("第一行 ifName = %q", v.Str)
	}
	if v, _ := first.Get("ifAlias"); v.Str != "to-srv-a3f-01-01-nic0" {
		t.Fatalf("第一行 ifAlias = %q", v.Str)
	}
	// 样本里 Ethernet1/3 是 admin up / oper down。
	third := sorted[2]
	if v, _ := third.Get("ifOperStatus"); v.Num != 2 {
		t.Fatalf("第 3 行（Ethernet1/3）的 oper 应为 2，实际 %d", v.Num)
	}
	if _, ok := first.Get("ifIndex"); ok {
		t.Fatal("ifIndex 不该出现在列值里，它是行索引")
	}
}

// 两张表靠 ifIndex 合并：ifXTable 的 5 列要落到同一行上。
func TestRowSetMergeIn(t *testing.T) {
	a := loadAgent(t, "spine-01")
	ifRows, ifxRows := RowSet{}, RowSet{}
	for _, col := range Columns {
		target := &ifRows
		if col.Table == "ifXTable" {
			target = &ifxRows
		}
		var st WalkStats
		vbs, err := WalkColumn(col, 64, fakeRequest(t, a, &st), &st)
		if err != nil {
			t.Fatalf("列 %s: %v", col.Name, err)
		}
		target.Add(col, vbs)
	}
	if len(ifRows) != 24 || len(ifxRows) != 24 {
		t.Fatalf("两张表各 24 行，实际 %d / %d", len(ifRows), len(ifxRows))
	}
	ifRows.MergeIn(ifxRows)
	rows := ifRows.Sorted()
	if len(rows) != 24 {
		t.Fatalf("合并后 = %d 行，期望 24（并集，不是相加）", len(rows))
	}
	if v, ok := rows[0].Get("ifName"); !ok || v.Str != "Ethernet1/1" {
		t.Fatalf("合并后缺 ifXTable 的列: %+v", rows[0].Values)
	}
	if v, ok := rows[0].Get("ifHighSpeed"); !ok || v.Num != 100000 {
		t.Fatalf("合并后 ifHighSpeed = %+v", v)
	}
}
