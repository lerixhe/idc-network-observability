package main

import (
	"math"
	"strings"
	"testing"
)

func TestAttributeFourOwners(t *testing.T) {
	// 每一条都是 samples/errors.json 里真实存在的那一格，数字抄过来当断言。
	cases := []struct {
		name  string
		c     ErrCounters
		owner string
		want  string // 证据里必须出现的一个片段
	}{
		{
			name: "线缆：帧校验失败成片，符号错几乎没有",
			c: ErrCounters{Device: "a", IfName: "e1", WindowS: 300, SpeedBps: 1e9,
				InUcastPkts: 46_800_000, InOctets: 22_500_000_000, FcsErrors: 4210, SymbolErrors: 12},
			owner: OwnerCable,
			want:  "编码层干净",
		},
		{
			name: "模块：符号错是帧错的 1333 倍",
			c: ErrCounters{Device: "a", IfName: "e2", WindowS: 300, SpeedBps: 1e10,
				InUcastPkts: 90_000_000, FcsErrors: 1800, SymbolErrors: 2_400_000},
			owner: OwnerModule,
			want:  "编码层在成片出错",
		},
		{
			name: "拥塞（出向队列）",
			c: ErrCounters{Device: "a", IfName: "e3", WindowS: 300, SpeedBps: 1e10,
				InUcastPkts: 225_000_000, OutQueueDiscards: 880_000, OutDiscards: 900_000},
			owner: OwnerCongestion,
			want:  "队列满了",
		},
		{
			name: "拥塞（入向缓存）",
			c: ErrCounters{Device: "a", IfName: "e4", WindowS: 300, SpeedBps: 25e9,
				InUcastPkts: 60_000_000, InBufferDiscards: 120_000},
			owner: OwnerCongestion,
			want:  "入口缓存被超订打穿",
		},
		{
			name: "主机：交换机一个错都没有，主机在丢",
			c: ErrCounters{Device: "a", IfName: "e5", WindowS: 300, SpeedBps: 1e10,
				InUcastPkts: 30_000_000, HostRxDrops: 33_000, HostRetransmits: 8400},
			owner: OwnerHost,
			want:  "主机报了 33000 个接收丢弃",
		},
		{
			name: "干净：三个丢弃不到 1 ppm",
			c: ErrCounters{Device: "a", IfName: "e6", WindowS: 300, SpeedBps: 1e10,
				InUcastPkts: 150_000_000, OutDiscards: 3},
			owner: OwnerClean,
			want:  "当噪声",
		},
	}
	for _, c := range cases {
		a := Attribute(c.c)
		if a.Owner != c.owner {
			t.Errorf("%s：归因 %q，期望 %q（证据 %v）", c.name, a.Owner, c.owner, a.Reasons)
			continue
		}
		if len(a.Reasons) == 0 || !containsAny(a.Reasons, c.want) {
			t.Errorf("%s：证据里没有 %q，实际 %v", c.name, c.want, a.Reasons)
		}
		if a.NextStep == "" {
			t.Errorf("%s：归因给了结论却没说下一步做什么", c.name)
		}
	}
}

// TestAttributePhysicalAndCongestionTogether 覆盖「修完物理层还有事」：主结论只写一个，
// 次要发现必须单独列出来，否则读者以为换完纤就收工。
func TestAttributePhysicalAndCongestionTogether(t *testing.T) {
	a := Attribute(ErrCounters{
		Device: "sw", IfName: "e1", WindowS: 300, SpeedBps: 1e10,
		InUcastPkts: 200_000_000, FcsErrors: 900, SymbolErrors: 20,
		OutQueueDiscards: 60_000, OutDiscards: 61_000,
	})
	if a.Owner != OwnerCable {
		t.Fatalf("主归因 = %q，期望线缆（物理层优先）", a.Owner)
	}
	if len(a.Also) == 0 || !containsAny(a.Also, "拥塞问题还在") {
		t.Fatalf("次要发现里没有提拥塞：%v", a.Also)
	}
}

// TestAttributeRatioDecidesCableOrModule：符号错与坏帧的比值是线缆和模块的分界。
func TestAttributeRatioDecidesCableOrModule(t *testing.T) {
	base := ErrCounters{Device: "sw", IfName: "e1", WindowS: 300, SpeedBps: 1e10, InUcastPkts: 10_000_000, FcsErrors: 1000}

	justBelow := base
	justBelow.SymbolErrors = uint64(symbolFrameRatio-1) * base.FcsErrors
	if got := Attribute(justBelow).Owner; got != OwnerCable {
		t.Errorf("比值 %d 落在阈值下面，应当判线缆，实际 %q", symbolFrameRatio-1, got)
	}

	justAt := base
	justAt.SymbolErrors = uint64(symbolFrameRatio) * base.FcsErrors
	if got := Attribute(justAt).Owner; got != OwnerModule {
		t.Errorf("比值正好到阈值 %d，应当判模块，实际 %q", symbolFrameRatio, got)
	}
}

// TestAttributePPMArithmetic 把每百万帧的算术钉住：读者要能自己代入。
func TestAttributePPMArithmetic(t *testing.T) {
	a := Attribute(ErrCounters{
		Device: "sw", IfName: "e1", WindowS: 300, SpeedBps: 1e9,
		InOctets: 22_500_000_000, InUcastPkts: 46_800_000, FcsErrors: 4210,
	})
	// 4210 / 46,800,000 × 1e6 = 89.96 ppm
	if math.Abs(a.FramePPM-89.957) > 0.01 {
		t.Errorf("帧错率 = %.3f ppm，期望 89.957", a.FramePPM)
	}
	// 22.5e9 字节 × 8 / (1e9 × 300s) = 60%
	if math.Abs(a.UtilPct-60) > 0.01 {
		t.Errorf("利用率 = %.2f%%，期望 60%%", a.UtilPct)
	}
	if a.SymbolPerErr > 0.01 {
		t.Errorf("每坏帧符号错 = %.4f，期望接近 0", a.SymbolPerErr)
	}
}

// TestAttributeReplaceThreshold 把换件线钉住：90 ppm 只是换件，不是观察。
func TestAttributeReplaceThreshold(t *testing.T) {
	c := ErrCounters{Device: "sw", IfName: "e1", WindowS: 300, SpeedBps: 1e9,
		InUcastPkts: 46_800_000, FcsErrors: 6000, SymbolErrors: 12} // 128 ppm
	a := Attribute(c)
	if a.FramePPM < frameErrorReplacePPM {
		t.Fatalf("这份样本的帧错率 %.1f ppm 应当在换件线上方", a.FramePPM)
	}
	if !containsAny(a.Also, "换件线") {
		t.Errorf("帧错率超过 %.0f ppm 却没提换件线：%v", frameErrorReplacePPM, a.Also)
	}

	// 反过来：低到 1 ppm 以下的帧错率只配观察。
	c2 := c
	c2.FcsErrors = 20 // 20 / 46800000 = 0.43 ppm
	b := Attribute(c2)
	if b.FramePPM >= visiblePPM {
		t.Fatalf("帧错率 %.2f ppm 应当在观察线下", b.FramePPM)
	}
	if !containsAny(b.Also, "可以观察") {
		t.Errorf("低帧错率没给出手观察的提示：%v", b.Also)
	}
}

// TestAttributeMissingWindow 挡住没有分母的归因。
func TestAttributeMissingWindow(t *testing.T) {
	a := Attribute(ErrCounters{Device: "sw", IfName: "e1", FcsErrors: 100})
	if a.Owner != OwnerClean || a.Verdict != "无法归因" {
		t.Fatalf("缺窗口长度时应当拒绝归因，实际 owner=%q verdict=%q", a.Owner, a.Verdict)
	}
}

func TestLoadErrCounters(t *testing.T) {
	ports, err := loadErrCounters("samples/errors.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 7 {
		t.Fatalf("样本里有 %d 个端口，期望 7", len(ports))
	}
	owners := map[string]int{}
	for _, c := range ports {
		owners[Attribute(c).Owner]++
	}
	for _, want := range []string{OwnerCable, OwnerModule, OwnerCongestion, OwnerHost, OwnerClean} {
		if owners[want] == 0 {
			t.Errorf("样本里没有 %s 这一类的端口，四类归因覆盖不全：%v", want, owners)
		}
	}
}

func containsAny(items []string, sub string) bool {
	for _, s := range items {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
