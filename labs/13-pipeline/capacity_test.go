package main

import (
	"math"
	"testing"
	"time"
)

func baseCapacityInput() CapacityInput {
	return CapacityInput{
		PointsPerSecond:  1200,
		BytesPerPoint:    10,
		CompressRatio:    2,
		Replicas:         3,
		WriteAmp:         2,
		PricePerTBYear:   1000,
		DetailInterval:   10 * time.Second,
		AggBytesFactor:   2,
		RetainDetailDays: 10,
		Retain1mDays:     100,
		Retain5mDays:     1000,
	}
}

func TestCapacityFormulas(t *testing.T) {
	in := baseCapacityInput()
	rep := estimateCapacity(in)

	if rep.Series != 12000 {
		t.Fatalf("序列数 = 每秒点数 × 采样间隔 = 12000，实际 %v", rep.Series)
	}
	detail, oneMin, fiveMin := rep.Tiers[0], rep.Tiers[1], rep.Tiers[2]

	// 明细层：1200 点/秒 × 10 字节 × 86400 秒 × 10 天
	wantRaw := 1200.0 * 10 * 86400 * 10
	if math.Abs(detail.RawBytes-wantRaw) > 1 {
		t.Fatalf("明细原始字节 %v ≠ %v", detail.RawBytes, wantRaw)
	}
	// 落盘 = 原始 ÷ 压缩比 × 副本 × 写放大
	wantStored := wantRaw / 2 * 3 * 2
	if math.Abs(detail.StoredBytes-wantStored) > 1 {
		t.Fatalf("明细落盘字节 %v ≠ %v", detail.StoredBytes, wantStored)
	}

	// 聚合层每序列一分钟一个点：12000/60 = 200 点/秒，每点 20 字节
	if math.Abs(oneMin.PointsPerSecond-200) > 1e-9 {
		t.Fatalf("1 分钟层点/秒 %v ≠ 200", oneMin.PointsPerSecond)
	}
	if math.Abs(fiveMin.PointsPerSecond-40) > 1e-9 {
		t.Fatalf("5 分钟层点/秒 %v ≠ 40", fiveMin.PointsPerSecond)
	}
	if oneMin.BytesPerPoint != 20 || fiveMin.BytesPerPoint != 20 {
		t.Fatalf("聚合层每点字节应是明细的 2 倍：%v / %v", oneMin.BytesPerPoint, fiveMin.BytesPerPoint)
	}

	total := detail.CostPerYear + oneMin.CostPerYear + fiveMin.CostPerYear
	if math.Abs(rep.TotalCostPerYear-total) > 1e-6 {
		t.Fatalf("三层合计 %v ≠ 分项之和 %v", rep.TotalCostPerYear, total)
	}
	if math.Abs(rep.TotalStoredBytes-(detail.StoredBytes+oneMin.StoredBytes+fiveMin.StoredBytes)) > 1 {
		t.Fatal("三层落盘字节合计不对")
	}
}

func TestCapacityDetailOneYearCostsMore(t *testing.T) {
	rep := estimateCapacity(baseCapacityInput())
	if rep.DetailOneYear.RetentionDays != 365 {
		t.Fatalf("「留一年」那行的保留天数 %d ≠ 365", rep.DetailOneYear.RetentionDays)
	}
	if rep.DetailExtraCost <= 0 {
		t.Fatalf("明细从 10 天留到一年应该更贵，实际增量 %v", rep.DetailExtraCost)
	}
	if rep.DetailOneYear.CostPerYear <= rep.Tiers[0].CostPerYear {
		t.Fatal("留一年不该比留 10 天便宜")
	}
}

// 压缩比翻倍，成本减半——这是容量估算里唯一能打折的参数。
func TestCapacityCompressionHalvesCost(t *testing.T) {
	in := baseCapacityInput()
	a := estimateCapacity(in)
	in.CompressRatio *= 2
	b := estimateCapacity(in)
	if math.Abs(b.TotalCostPerYear*2-a.TotalCostPerYear) > 1e-6 {
		t.Fatalf("压缩比翻倍后成本 %v，期望是 %v 的一半", b.TotalCostPerYear, a.TotalCostPerYear)
	}
}
