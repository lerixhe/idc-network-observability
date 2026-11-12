package main

import (
	"fmt"
	"math"
	"testing"
)

func TestProbeWeightRules(t *testing.T) {
	// 规则顺序：同机架不探压过一切，其次上行必探，最后才查表抽样。
	cases := []struct {
		name     string
		src, dst Node
		want     float64
	}{
		{"自己探自己", Node{Name: "srv-1a", Role: roleServer, Rack: "rack-1"}, Node{Name: "srv-1a", Role: roleServer, Rack: "rack-1"}, 0},
		{"同机架两台服务器不探", Node{Name: "srv-1a", Role: roleServer, Rack: "rack-1"}, Node{Name: "srv-1b", Role: roleServer, Rack: "rack-1"}, 0},
		{"服务器不探本机架 TOR（上行也让位给同架规则）", Node{Name: "srv-1a", Role: roleServer, Rack: "rack-1"}, Node{Name: "tor-1", Role: roleTOR, Rack: "rack-1"}, 0},
		{"服务器探 spine 是上行必探", Node{Name: "srv-1a", Role: roleServer, Rack: "rack-1"}, Node{Name: "spine-1", Role: roleSpine, Pod: "pod-a"}, 1},
		{"服务器探 border 是上行必探", Node{Name: "srv-1a", Role: roleServer, Rack: "rack-1"}, Node{Name: "border-01", Role: roleBorder}, 1},
		{"跨机架服务器全探", Node{Name: "srv-1a", Role: roleServer, Rack: "rack-1"}, Node{Name: "srv-2a", Role: roleServer, Rack: "rack-2"}, 1},
		{"服务器探别的机架 TOR 抽一半", Node{Name: "srv-1a", Role: roleServer, Rack: "rack-1"}, Node{Name: "tor-2", Role: roleTOR, Rack: "rack-2"}, 0.5},
		{"TOR 探 spine 上行必探", Node{Name: "tor-1", Role: roleTOR, Rack: "rack-1"}, Node{Name: "spine-1", Role: roleSpine, Pod: "pod-a"}, 1},
		{"TOR 探 border 是上行必探", Node{Name: "tor-1", Role: roleTOR, Rack: "rack-1"}, Node{Name: "border-01", Role: roleBorder}, 1},
		{"TOR 之间抽一成", Node{Name: "tor-1", Role: roleTOR, Rack: "rack-1"}, Node{Name: "tor-2", Role: roleTOR, Rack: "rack-2"}, 0.1},
		{"spine 探 border 上行必探", Node{Name: "spine-1", Role: roleSpine, Pod: "pod-a"}, Node{Name: "border-01", Role: roleBorder}, 1},
		{"spine 之间不探", Node{Name: "spine-1", Role: roleSpine, Pod: "pod-a"}, Node{Name: "spine-2", Role: roleSpine, Pod: "pod-a"}, 0},
		{"border 探 spine 是下行抽样", Node{Name: "border-01", Role: roleBorder}, Node{Name: "spine-1", Role: roleSpine, Pod: "pod-a"}, 0.25},
		{"border 之间不探（跨机房出口另说）", Node{Name: "border-01", Role: roleBorder}, Node{Name: "border-02", Role: roleBorder}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := probeWeight(c.src, c.dst); got != c.want {
				t.Fatalf("权重 = %v，期望 %v", got, c.want)
			}
		})
	}
}

func TestSampleHitRatio(t *testing.T) {
	// 0.1 的比例在 1000 条候选上应该落在 60~140。范围放宽是因为这里考的是
	// 「比例大致对」，不是二项分布的置信区间。
	n := 0
	for i := 0; i < 1000; i++ {
		if sampleHit("src", fmt.Sprintf("dst-%03d", i), 0.1) {
			n++
		}
	}
	if n < 60 || n > 140 {
		t.Fatalf("0.1 的比例命中 %d/1000，抽样明显有偏", n)
	}
	if sampleHit("a", "b", 0) || !sampleHit("a", "b", 1) {
		t.Fatal("0 应该全不探，1 应该全探")
	}
}

func TestGenerateMatrixOnSample(t *testing.T) {
	tp, err := LoadTopology("samples/topology.json")
	if err != nil {
		t.Fatalf("加载拓扑失败：%v", err)
	}
	m, err := GenerateMatrix(tp, 1)
	if err != nil {
		t.Fatalf("生成矩阵失败：%v", err)
	}

	if m.OrderedPairs != 36*35 {
		t.Fatalf("全量有序对 = %d，期望 %d", m.OrderedPairs, 36*35)
	}
	if len(m.Pairs) >= m.OrderedPairs {
		t.Fatalf("矩阵 %d 对，没有比全量 %d 对更省，规则没生效", len(m.Pairs), m.OrderedPairs)
	}
	if m.NodeCovered != m.NodeTotal {
		t.Fatalf("节点覆盖 %d/%d，样本里每个节点都该被探到", m.NodeCovered, m.NodeTotal)
	}
	if m.LinkRate() < 0.90 {
		t.Fatalf("链路覆盖 %.1f%%，规则应该能覆盖九成以上", m.LinkRate()*100)
	}

	// 每一对都必须有路径，且路径非空、不重复。
	for _, p := range m.Pairs {
		if len(p.Path) == 0 {
			t.Fatalf("%s 没有路径", pairKey(p.Src, p.Dst))
		}
		seen := map[string]bool{}
		for _, k := range p.Path {
			if seen[k] {
				t.Fatalf("%s 的路径里 %s 出现了两次：%v", pairKey(p.Src, p.Dst), k, p.Path)
			}
			seen[k] = true
		}
	}

	// 同机架的对一条都不许有。
	rack := map[string]string{}
	for _, n := range tp.Nodes {
		rack[n.Name] = n.Rack
	}
	for _, p := range m.Pairs {
		if rack[p.Src] != "" && rack[p.Src] == rack[p.Dst] {
			t.Fatalf("同机架的对被选中了：%s", pairKey(p.Src, p.Dst))
		}
	}
}

func TestGenerateMatrixIsDeterministic(t *testing.T) {
	tp := miniTopology(t)
	a, err := GenerateMatrix(tp, 1)
	if err != nil {
		t.Fatalf("第一次生成失败：%v", err)
	}
	b, err := GenerateMatrix(tp, 1)
	if err != nil {
		t.Fatalf("第二次生成失败：%v", err)
	}
	if len(a.Pairs) != len(b.Pairs) {
		t.Fatalf("两次生成的对数不同：%d vs %d", len(a.Pairs), len(b.Pairs))
	}
	for i := range a.Pairs {
		if a.Pairs[i].Src != b.Pairs[i].Src || a.Pairs[i].Dst != b.Pairs[i].Dst {
			t.Fatalf("第 %d 对不同：%v vs %v", i, a.Pairs[i], b.Pairs[i])
		}
	}
}

func TestCostFormulas(t *testing.T) {
	tp := miniTopology(t)
	m, err := GenerateMatrix(tp, 10)
	if err != nil {
		t.Fatalf("生成矩阵失败：%v", err)
	}
	c := m.Cost

	wantPps := float64(len(m.Pairs)) / 10
	if math.Abs(c.Pps-wantPps) > 1e-9 {
		t.Fatalf("包速率 = %v，期望 %v", c.Pps, wantPps)
	}
	wantKbit := wantPps * probePacketBytes * 8 / 1000
	if math.Abs(c.KbitPerS-wantKbit) > 1e-9 {
		t.Fatalf("带宽 = %v kbit/s，期望 %v", c.KbitPerS, wantKbit)
	}
	// mini 拓扑总带宽：400 + 100×2 + 25×4 = 700 Gbps
	if math.Abs(c.TotalBWGbps-700) > 1e-9 {
		t.Fatalf("总带宽 = %v Gbps，期望 700", c.TotalBWGbps)
	}
	wantRecords := float64(len(m.Pairs)) * 86400 / aggWindowS
	if math.Abs(c.RecordsPerDay-wantRecords) > 1e-6 {
		t.Fatalf("记录数/天 = %v，期望 %v", c.RecordsPerDay, wantRecords)
	}
	if math.Abs(c.AggBytesPerDay-wantRecords*aggRecordBytes) > 1e-3 {
		t.Fatalf("聚合存储/天 = %v，期望 %v", c.AggBytesPerDay, wantRecords*aggRecordBytes)
	}

	// 间隔翻倍，包速率与带宽减半，存储不变（存储按聚合窗口算，与间隔无关）。
	m2, err := GenerateMatrix(tp, 20)
	if err != nil {
		t.Fatalf("第二次生成失败：%v", err)
	}
	if math.Abs(m2.Cost.Pps*2-c.Pps) > 1e-9 {
		t.Fatalf("间隔翻倍后包速率没减半：%v vs %v", m2.Cost.Pps, c.Pps)
	}
	if math.Abs(m2.Cost.AggBytesPerDay-c.AggBytesPerDay) > 1e-3 {
		t.Fatalf("聚合存储不该随间隔变：%v vs %v", m2.Cost.AggBytesPerDay, c.AggBytesPerDay)
	}
	if m2.Cost.RawBytesPerDay >= c.RawBytesPerDay {
		t.Fatal("间隔变长，逐包原始存储应该变小")
	}
}
