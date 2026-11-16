package main

import "testing"

const fixturePath = "samples/case.json"

// 三种策略的结论必须一致，而且都要对上注入的真值段。
func TestStrategiesHitTruthSegment(t *testing.T) {
	f, err := LoadFixture(fixturePath)
	if err != nil {
		t.Fatalf("读场景失败：%v", err)
	}

	covered, holes := 0, 0
	for _, c := range f.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			results := locate(f.Segments, c.Samples, f.Baseline, f.PerProbeOverheadMs)
			if len(results) != 3 {
				t.Fatalf("策略数 = %d，期望 3", len(results))
			}
			switch {
			case c.FaultIndex < 0:
				for _, r := range results {
					if r.Found || r.NeedProbe {
						t.Fatalf("%s：无故障场景下报了 %s", r.Strategy, r.verdict(f.Segments))
					}
				}
			case !c.Samples[c.FaultIndex].Covered:
				for _, r := range results {
					if !r.NeedProbe {
						t.Fatalf("%s：故障段无覆盖，应报需补探针，实际 %s", r.Strategy, r.verdict(f.Segments))
					}
					if len(r.Holes) != 1 || r.Holes[0] != f.Segments[c.FaultIndex] {
						t.Fatalf("%s：覆盖缺口 = %v，期望 [%s]", r.Strategy, r.Holes, f.Segments[c.FaultIndex])
					}
				}
			default:
				for _, r := range results {
					if !r.Found {
						t.Fatalf("%s：应命中 %s，实际 %s", r.Strategy, f.Segments[c.FaultIndex], r.verdict(f.Segments))
					}
					if r.FoundIndex != c.FaultIndex {
						t.Fatalf("%s：命中 %s，真值 %s", r.Strategy, f.Segments[r.FoundIndex], f.Segments[c.FaultIndex])
					}
				}
			}

			// 记账：探测次数非零、耗时为非、次数不超过「段数 + 复核一次」。
			for _, r := range results {
				if r.Probes == 0 || r.Probes > len(c.Samples)+1 {
					t.Fatalf("%s：探测次数 %d 越界（段数 %d）", r.Strategy, r.Probes, len(c.Samples))
				}
				if r.TotalMs <= 0 {
					t.Fatalf("%s：总耗时 %d，应为正", r.Strategy, r.TotalMs)
				}
			}

			// 顺序扫逐段点名，覆盖场景下探测次数正好等于故障下标 + 1。
			if c.FaultIndex >= 0 && c.Samples[c.FaultIndex].Covered && results[0].Probes != c.FaultIndex+1 {
				t.Fatalf("顺序扫探测 %d 次，期望 %d", results[0].Probes, c.FaultIndex+1)
			}

			if c.FaultIndex >= 0 && !c.Samples[c.FaultIndex].Covered {
				holes++
			} else {
				covered++
			}
		})
	}

	if covered == 0 || holes == 0 {
		t.Fatalf("场景覆盖不足：命中场景 %d 个、无覆盖场景 %d 个", covered, holes)
	}
}

func TestPrefixProbeCostGrows(t *testing.T) {
	base := Baseline{LatencyMs: 3, LossPct: 0.02}
	samples := []Sample{
		{Covered: true, LatencyMs: 3, ProbeCostMs: 4},
		{Covered: true, LatencyMs: 3, ProbeCostMs: 6},
		{Covered: true, LatencyMs: 3, ProbeCostMs: 12},
	}
	p := &prober{baseline: base, samples: samples, overheadMs: 5}

	p.probePrefix(0)
	first := p.spentMs
	p.probePrefix(2)
	if p.spentMs-first <= first {
		t.Fatalf("末位前缀应比首位前缀贵，实际 %d vs %d", p.spentMs-first, first)
	}
	if p.probes != 2 {
		t.Fatalf("探测次数 = %d，期望 2", p.probes)
	}
}

// 覆盖缺口不能被当成健康：无样本的段一律算 legUncovered。
func TestUncoveredIsNotHealthy(t *testing.T) {
	base := Baseline{LatencyMs: 3, LossPct: 0.02}
	p := &prober{baseline: base, samples: []Sample{{Covered: false}}}
	if got := p.classify(0); got != legUncovered {
		t.Fatalf("无覆盖段应判为 legUncovered，实际 %v", got)
	}
}
