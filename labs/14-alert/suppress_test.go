package main

import (
	"math"
	"testing"
)

// mkAlert 造一条告警并顺手把时间戳解析好。测试里不经过 JSON，这一步得自己补。
func mkAlert(t *testing.T, id, ts, resolve, obj, class, sev, truth string) Alert {
	t.Helper()
	a := Alert{ID: id, TS: ts, ResolveTS: resolve, Source: "trap",
		Obj: obj, Class: class, Severity: sev, Truth: truth}
	var err error
	a.tsMs, err = parseTS(ts)
	if err != nil {
		t.Fatalf("%s 的 ts: %v", id, err)
	}
	if resolve != "" {
		a.resolveMs, err = parseTS(resolve)
		if err != nil {
			t.Fatalf("%s 的 resolve_ts: %v", id, err)
		}
	}
	return a
}

func outcomeOf(t *testing.T, res SuppressResult, id string) Outcome {
	t.Helper()
	for _, o := range res.Outcomes {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("结果里没有 %s", id)
	return Outcome{}
}

// 父告警在，子告警就被压住，而且必须说得出是被谁压的。
// 这是「可追溯」的全部含义：说不出父是谁的抑制，等于把告警删了。
func TestParentSuppressesChild(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		mkAlert(t, "p1", "2026-05-12T10:00:00Z", "2026-05-12T10:10:00Z",
			"device:spine-01", ClassDeviceReset, "critical", TruthRoot),
		mkAlert(t, "c1", "2026-05-12T10:00:10Z", "2026-05-12T10:09:00Z",
			"port:spine-01:Ethernet1/1", ClassPortDown, "critical", TruthChild),
	}

	res := Suppress(tree, alerts, defaultSuppressPolicy)

	if res.Alerts != 2 || res.Suppressed != 1 {
		t.Fatalf("压掉 %d 条，想要 1 条（共 %d 条）", res.Suppressed, res.Alerts)
	}
	c := outcomeOf(t, res, "c1")
	if !c.Suppressed {
		t.Fatal("c1 没被压")
	}
	if c.SuppressedBy != "p1" {
		t.Fatalf("c1 被 %q 压，想要 p1", c.SuppressedBy)
	}
	if c.SuppressedByObj != "device:spine-01" {
		t.Fatalf("父对象 %q", c.SuppressedByObj)
	}
	if len(c.SuppressChain) != 1 || c.SuppressChain[0] != "p1" {
		t.Fatalf("压制链 %v", c.SuppressChain)
	}
	// 子先恢复：父还在的时候就没了，不需要补发。
	if c.ResumedAt != "" {
		t.Fatalf("子先恢复却打了补发标记：%q", c.ResumedAt)
	}
	if res.NetConverged != 1 {
		t.Fatalf("净收敛 %d 条，想要 1 条", res.NetConverged)
	}
	if res.Emitted != 1 {
		t.Fatalf("投递 %d 条，想要 1 条", res.Emitted)
	}
	if math.Abs(res.Ratio()-2.0) > 1e-9 {
		t.Fatalf("收敛比 %.3f，想要 2.0", res.Ratio())
	}
}

// 父告警消失后，还在 firing 的子告警必须补发。
// 这条断言是「不能永久吞掉」的底线：告警可以晚到，不能不来。
func TestChildResumesAfterParentGone(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		mkAlert(t, "p1", "2026-05-12T10:00:00Z", "2026-05-12T10:05:00Z",
			"device:spine-01", ClassDeviceReset, "critical", TruthRoot),
		// 父 10:05 消失，子一直烧到 10:20，比 120 秒宽限长得多。
		mkAlert(t, "c1", "2026-05-12T10:00:10Z", "2026-05-12T10:20:00Z",
			"port:spine-01:Ethernet1/1", ClassPortDown, "critical", TruthChild),
	}

	res := Suppress(tree, alerts, defaultSuppressPolicy)
	c := outcomeOf(t, res, "c1")

	if !c.Suppressed || c.SuppressedBy != "p1" {
		t.Fatalf("c1 该被 p1 压：%+v", c)
	}
	if c.ResumedAt != "2026-05-12T10:05:00Z" {
		t.Fatalf("补发时刻 %q，想要父消失那一刻 2026-05-12T10:05:00Z", c.ResumedAt)
	}
	if res.Resumed != 1 || res.NetConverged != 0 {
		t.Fatalf("补发 %d 条、净收敛 %d 条，想要 1 和 0", res.Resumed, res.NetConverged)
	}
	if res.Emitted != 2 {
		t.Fatalf("投递 %d 条，想要 2 条（父 + 补发的子）", res.Emitted)
	}
}

// 父消失后子也很快恢复，是同一件事的尾巴，不补发。
func TestChildRestoredWithinGraceIsNotResumed(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		mkAlert(t, "p1", "2026-05-12T10:00:00Z", "2026-05-12T10:05:00Z",
			"device:spine-01", ClassDeviceReset, "critical", TruthRoot),
		// 父 10:05 消失，子 10:06 就恢复，落在 120 秒宽限内。
		mkAlert(t, "c1", "2026-05-12T10:00:10Z", "2026-05-12T10:06:00Z",
			"port:spine-01:Ethernet1/1", ClassPortDown, "critical", TruthChild),
	}

	res := Suppress(tree, alerts, defaultSuppressPolicy)
	c := outcomeOf(t, res, "c1")
	if c.ResumedAt != "" {
		t.Fatalf("宽限内恢复的告警被补发了：%q", c.ResumedAt)
	}
	if !c.RestoredGrace {
		t.Fatal("没打上「宽限内恢复」的标记")
	}
	if res.NetConverged != 1 || res.Emitted != 1 {
		t.Fatalf("净收敛 %d、投递 %d，想要 1 和 1", res.NetConverged, res.Emitted)
	}
}

// 抑制是单向的：父压制子，子永远压不住父。
func TestChildNeverSuppressesParent(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		mkAlert(t, "c1", "2026-05-12T10:00:00Z", "2026-05-12T10:10:00Z",
			"port:spine-01:Ethernet1/1", ClassPortDown, "critical", TruthChild),
		mkAlert(t, "p1", "2026-05-12T10:00:10Z", "2026-05-12T10:10:00Z",
			"device:spine-01", ClassDeviceReset, "critical", TruthRoot),
	}

	res := Suppress(tree, alerts, defaultSuppressPolicy)
	for _, id := range []string{"c1", "p1"} {
		if o := outcomeOf(t, res, id); o.Suppressed {
			t.Fatalf("%s 被 %s 压了——方向反了", id, o.SuppressedBy)
		}
	}
	if res.Emitted != 2 {
		t.Fatalf("投递 %d 条，想要 2 条", res.Emitted)
	}
}

// 子比父早出现，说明这两条不是因果，是并发。时间闸拦的就是这个。
func TestLagGateBlocksLateParent(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		// 端口先 down，设备 5 秒后才报重启——后者不可能是前者的原因。
		mkAlert(t, "c1", "2026-05-12T10:00:00Z", "2026-05-12T10:10:00Z",
			"port:spine-01:Ethernet1/1", ClassPortDown, "critical", TruthChild),
		mkAlert(t, "p1", "2026-05-12T10:00:05Z", "2026-05-12T10:10:00Z",
			"device:spine-01", ClassDeviceReset, "critical", TruthRoot),
	}
	if res := Suppress(tree, alerts, defaultSuppressPolicy); res.Suppressed != 0 {
		t.Fatalf("晚到的父压住了先到的子，压掉 %d 条", res.Suppressed)
	}

	// 把闸拆掉（lag=0）也压不住：父仍然比子晚。
	loose := defaultSuppressPolicy
	loose.LagMs = 0
	if res := Suppress(tree, alerts, loose); res.Suppressed != 0 {
		t.Fatalf("lag=0 时晚到的父也能压，压掉 %d 条", res.Suppressed)
	}
}

// 类型不能解释就不压。这一道闸是唯一拦住「拿拥塞压重启」的东西。
func TestExplainTableBlocksUnrelatedClass(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		mkAlert(t, "p1", "2026-05-12T10:00:00Z", "2026-05-12T10:10:00Z",
			"device:spine-01", ClassCongestion, "critical", TruthIndependent),
		mkAlert(t, "c1", "2026-05-12T10:00:20Z", "2026-05-12T10:10:00Z",
			"port:spine-01:Ethernet1/1", ClassDeviceReset, "critical", TruthRoot),
	}

	res := Suppress(tree, alerts, defaultSuppressPolicy)
	if o := outcomeOf(t, res, "c1"); o.Suppressed {
		t.Fatalf("拥塞告警压住了设备重启，父 %s", o.SuppressedBy)
	}

	loose := defaultSuppressPolicy
	loose.ExplainRequired = false
	res = Suppress(tree, alerts, loose)
	if o := outcomeOf(t, res, "c1"); !o.Suppressed {
		t.Fatal("拆掉类型闸之后仍然没压，说明这条断言测的不是类型闸")
	}
}

func TestExplainTable(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{ClassDeviceReset, ClassPortDown, true},
		{ClassDeviceReset, ClassSvcDown, true},
		{ClassPortDown, ClassHostDown, true},
		{ClassCongestion, ClassPacketLoss, true},
		{ClassCongestion, ClassDeviceReset, false},
		{ClassDeviceReset, ClassDeviceReset, false},
		{ClassPortDown, ClassDeviceReset, false},
	}
	for _, c := range cases {
		if got := explains(c.parent, c.child); got != c.want {
			t.Errorf("explains(%s, %s) = %v，想要 %v", c.parent, c.child, got, c.want)
		}
	}
}

// 同层的两个端口谁也不是谁的原因。
func TestSiblingsDoNotSuppress(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		mkAlert(t, "a1", "2026-05-12T10:00:00Z", "2026-05-12T10:10:00Z",
			"port:spine-01:Ethernet1/1", ClassPortDown, "critical", TruthChild),
		mkAlert(t, "a2", "2026-05-12T10:00:05Z", "2026-05-12T10:10:00Z",
			"port:spine-01:Ethernet1/2", ClassPortDown, "critical", TruthChild),
	}
	res := Suppress(tree, alerts, defaultSuppressPolicy)
	if res.Suppressed != 0 {
		t.Fatalf("同层端口互压了 %d 条", res.Suppressed)
	}
}

// 拓扑里查不到父时，退回时间窗关联，并且必须打上 degraded 标记。
func TestDegradeToTimeWindow(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		mkAlert(t, "p1", "2026-05-12T10:00:00Z", "2026-05-12T10:10:00Z",
			"device:spine-01", ClassDeviceReset, "critical", TruthRoot),
		// 这台交换机不在台账里，没有血缘可用。
		mkAlert(t, "c1", "2026-05-12T10:00:20Z", "2026-05-12T10:09:00Z",
			"acc-a3f-77:Eth1/2", ClassPortDown, "critical", TruthIndependent),
	}

	res := Suppress(tree, alerts, defaultSuppressPolicy)
	c := outcomeOf(t, res, "c1")
	if !c.Orphan || c.OrphanReason != DegradeObjectUnknown {
		t.Fatalf("没标出血缘缺失：%+v", c)
	}
	if !c.Suppressed || !c.Degraded {
		t.Fatalf("时间窗没压住：%+v", c)
	}
	if c.SuppressedBy != "p1" {
		t.Fatalf("被 %q 压，想要 p1", c.SuppressedBy)
	}
	// 误伤：这条是另一件事，本来该自己报出去。
	if res.Collateral != 1 || res.DegradedCollateral != 1 {
		t.Fatalf("误伤计数 %d / 退化误伤 %d，都想要 1", res.Collateral, res.DegradedCollateral)
	}
}

// 退化路径的类型闸不能松。松了就变成「一分钟内谁红得早谁当爹」。
func TestDegradeStillRequiresExplain(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		mkAlert(t, "p1", "2026-05-12T10:00:00Z", "2026-05-12T10:10:00Z",
			"device:spine-01", ClassCongestion, "critical", TruthIndependent),
		mkAlert(t, "c1", "2026-05-12T10:00:20Z", "2026-05-12T10:09:00Z",
			"acc-a3f-77:Eth1/2", ClassDeviceReset, "critical", TruthRoot),
	}
	res := Suppress(tree, alerts, defaultSuppressPolicy)
	if o := outcomeOf(t, res, "c1"); o.Suppressed {
		t.Fatalf("退化路径拿拥塞压了重启，父 %s", o.SuppressedBy)
	}
}

// 离得太远的告警不是父，是巧合。
func TestDegradeWindowBounds(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		mkAlert(t, "p1", "2026-05-12T10:00:00Z", "2026-05-12T10:10:00Z",
			"device:spine-01", ClassDeviceReset, "critical", TruthRoot),
		// 20 秒：窗内。
		mkAlert(t, "near", "2026-05-12T10:00:20Z", "2026-05-12T10:09:00Z",
			"acc-a3f-77:Eth1/2", ClassPortDown, "critical", TruthChild),
		// 150 秒：窗外。
		mkAlert(t, "far", "2026-05-12T10:02:30Z", "2026-05-12T10:09:00Z",
			"acc-a3f-77:Eth1/3", ClassPortDown, "critical", TruthChild),
	}
	res := Suppress(tree, alerts, defaultSuppressPolicy)
	if o := outcomeOf(t, res, "near"); !o.Suppressed {
		t.Fatal("窗内的告警没被关联上")
	}
	if o := outcomeOf(t, res, "far"); o.Suppressed {
		t.Fatalf("窗外的告警被关联上了，父 %s", o.SuppressedBy)
	}
}

// 两个都查不到对象的告警不许互压：拿未知压未知，压完谁也说不清为什么。
func TestUnknownObjectsDoNotSuppressEachOther(t *testing.T) {
	tree := mustTree(t, demoNodes()...)
	alerts := []Alert{
		mkAlert(t, "x1", "2026-05-12T10:00:00Z", "2026-05-12T10:10:00Z",
			"acc-a3f-77:Eth1/2", ClassDeviceReset, "critical", TruthRoot),
		mkAlert(t, "x2", "2026-05-12T10:00:20Z", "2026-05-12T10:09:00Z",
			"acc-a3f-77:Eth1/3", ClassPortDown, "critical", TruthChild),
	}
	res := Suppress(tree, alerts, defaultSuppressPolicy)
	if res.Suppressed != 0 {
		t.Fatalf("未知对象之间互压了 %d 条", res.Suppressed)
	}
	if res.Orphans != 2 {
		t.Fatalf("血缘缺失计数 %d，想要 2", res.Orphans)
	}
}

// 收敛比的口径要能自己对上：原始 = 投递 + 净收敛，净收敛 = 压掉 - 补发。
func TestConvergenceRatioArithmetic(t *testing.T) {
	r := SuppressResult{Alerts: 87, Emitted: 5}
	if got := r.Ratio(); math.Abs(got-17.4) > 1e-9 {
		t.Fatalf("收敛比 %.4f，想要 17.4", got)
	}
	if got := r.SuppressRatio(); math.Abs(got-0.9425287) > 1e-6 {
		t.Fatalf("抑制率 %.6f", got)
	}
	if (SuppressResult{}).Ratio() != 0 {
		t.Fatal("空结果的收敛比应当是 0，不是 NaN")
	}
}

// 拿真样本跑一遍，把主剧本的行为钉住。数字变了就是行为变了，得说清为什么。
func TestSuppressMainScriptSample(t *testing.T) {
	tree, err := loadTree("samples/tree.json")
	if err != nil {
		t.Fatalf("读对象树: %v", err)
	}
	alerts, err := loadAlerts("samples/alerts.jsonl")
	if err != nil {
		t.Fatalf("读告警流: %v", err)
	}
	res := Suppress(tree, alerts, defaultSuppressPolicy)

	if res.Alerts != 87 {
		t.Fatalf("原始告警 %d 条，样本里是 87 条", res.Alerts)
	}
	if res.Emitted != 5 {
		t.Fatalf("投递 %d 条，想要 5 条", res.Emitted)
	}
	if res.Suppressed != 85 {
		t.Fatalf("压掉 %d 条，想要 85 条", res.Suppressed)
	}
	if res.Resumed != 3 {
		t.Fatalf("补发 %d 条，想要 3 条", res.Resumed)
	}
	if res.Alerts != res.Emitted+res.NetConverged {
		t.Fatalf("账不平：%d ≠ %d + %d", res.Alerts, res.Emitted, res.NetConverged)
	}
	if res.TopSuppressor != "a-0001" || res.TopSuppressorCnt != 85 {
		t.Fatalf("压得最多的是 %s（%d 条），想要 a-0001（85 条）",
			res.TopSuppressor, res.TopSuppressorCnt)
	}
	if res.Collateral != 1 {
		t.Fatalf("误伤 %d 条，样本里埋了 1 条", res.Collateral)
	}
	// 每条被压的告警都得说得出父是谁，否则审计到这里就断了。
	for _, o := range res.Outcomes {
		if o.Suppressed && o.SuppressedBy == "" {
			t.Fatalf("%s 被压了但没说被谁压", o.ID)
		}
	}
}

// 反例剧本：贪心口径会把真根因也压掉，严格口径不会。
func TestOverreachScriptCollateral(t *testing.T) {
	tree, err := loadTree("samples/tree.json")
	if err != nil {
		t.Fatalf("读对象树: %v", err)
	}
	alerts, err := loadAlerts("samples/alerts-overreach.jsonl")
	if err != nil {
		t.Fatalf("读告警流: %v", err)
	}

	strict := Suppress(tree, alerts, defaultSuppressPolicy)
	if strict.Collateral != 0 {
		t.Fatalf("严格口径误伤 %d 条，应为 0", strict.Collateral)
	}

	greedy := defaultSuppressPolicy
	greedy.LagMs = 0
	greedy.ExplainRequired = false
	loose := Suppress(tree, alerts, greedy)
	if loose.Collateral < 2 {
		t.Fatalf("贪心口径只误伤 %d 条，样本里至少埋了 2 条", loose.Collateral)
	}

	var roots []string
	for _, o := range loose.Outcomes {
		if o.Suppressed && o.Truth == TruthRoot {
			roots = append(roots, o.ID)
		}
	}
	if len(roots) != 1 || roots[0] != "o-0002" {
		t.Fatalf("被压掉的真根因是 %v，想要 [o-0002]", roots)
	}
	if loose.Emitted >= strict.Emitted {
		t.Fatalf("贪心口径没有多压：%d ≥ %d", loose.Emitted, strict.Emitted)
	}
}
