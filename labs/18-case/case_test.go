package main

import (
	"strings"
	"testing"
)

func baseCase(t *testing.T) Case {
	t.Helper()
	c, err := LoadCase("samples/case.json")
	if err != nil {
		t.Fatalf("读样本失败：%v", err)
	}
	return c
}

func TestReplayFindsNIC(t *testing.T) {
	c := baseCase(t)
	_, conclusion := Replay(c)
	if !strings.Contains(conclusion, "rx_fifo") && !strings.Contains(conclusion, "网卡接收侧") {
		t.Fatalf("结论应指向网卡接收侧，得到：%s", conclusion)
	}
}

func TestReplayTenSteps(t *testing.T) {
	c := baseCase(t)
	steps, _ := Replay(c)
	if len(steps) != 10 {
		t.Fatalf("应十步，得到 %d 步", len(steps))
	}
	for i, s := range steps {
		if s.No != i+1 {
			t.Errorf("第 %d 步编号错：%d", i+1, s.No)
		}
		if s.Ask == "" || s.Saw == "" || s.Verdict == "" {
			t.Errorf("第 %d 步有空字段：%+v", i+1, s)
		}
	}
}

// 改真值后结论必须跟着变，这条是这个实验最重要的断言：
// 回放逻辑不许硬编码结论。
func TestConclusionFollowsTruth(t *testing.T) {
	c := baseCase(t)
	_, before := Replay(c)

	// 把网卡丢包抹平：结论应变成「未定位」
	for i := range c.HostNICs {
		c.HostNICs[i].RxFifo = 0
		c.HostNICs[i].RxDropped = 0
	}
	_, after := Replay(c)
	if after == before {
		t.Fatal("抹平网卡计数后结论应变化，说明结论被硬编码了")
	}
	if !strings.Contains(after, "未定位") {
		t.Fatalf("应是未定位，得到：%s", after)
	}

	// 再抹平路径丢包：第 6 步应判定到此为止
	c2 := baseCase(t)
	for i := range c2.Paths {
		c2.Paths[i].LossPct = 0
	}
	steps, _ := Replay(c2)
	if !steps[5].Done {
		t.Fatalf("无丢包时第 6 步应判 done，得到 %+v", steps[5])
	}
}

func TestSwitchPortsCleanStillConcludes(t *testing.T) {
	c := baseCase(t)
	steps, _ := Replay(c)
	if !strings.Contains(steps[6].Saw, "异常 0 个") {
		t.Fatalf("样本里交换机应干净，得到：%s", steps[6].Saw)
	}
}

func TestEvidenceCollectsFacts(t *testing.T) {
	c := baseCase(t)
	ev := Evidence(c)
	if len(ev) < 3 {
		t.Fatalf("证据至少三条，得到 %v", ev)
	}
	joined := strings.Join(ev, "|")
	for _, want := range []string{"p99", "丢包", "rx_fifo_errors"} {
		if !strings.Contains(joined, want) {
			t.Errorf("证据缺少 %q：%v", want, ev)
		}
	}
}

func TestClockOffsetSurfaced(t *testing.T) {
	c := baseCase(t)
	steps, _ := Replay(c)
	last := steps[9]
	if !strings.Contains(last.Saw, "-41") {
		t.Fatalf("第 10 步应报出设备时钟偏差，得到：%s", last.Saw)
	}
}
