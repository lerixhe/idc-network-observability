package main

import "time"

// 三层保留的容量估算。
//
// 一条式子贯穿三层：
//
//	原始字节 = 每秒点数 × 每点字节 × 86400 × 保留天数
//	落盘字节 = 原始字节 ÷ 压缩比 × 副本数 × 写放大
//	年成本   = 落盘字节 ÷ 1e12 × 每 TB 年价
//
// 三层的每秒点数不是同一个数：明细层是「一个采样一个点」，聚合层是「一个序列一分钟一个点」。
// 序列数 = 每秒点数 × 明细采样间隔，这一步最容易算错——把明细的每秒点数直接当聚合层的，
// 会把 1 分钟层的容量高估 60 倍。

// CapacityInput 是估算的全部输入。每个参数都能改，改完结论自己会变。
type CapacityInput struct {
	PointsPerSecond  float64       // 明细层每秒点数
	BytesPerPoint    float64       // 明细层每点字节，默认取 PB1 实测
	CompressRatio    float64       // 列式压缩比，明细与聚合共用一个值（聚合实际更好压，这里偏保守）
	Replicas         int           // 副本数
	WriteAmp         float64       // 写放大：LSM 合并、索引、临时文件
	PricePerTBYear   float64       // 每 TB 每年，元
	DetailInterval   time.Duration // 明细采样间隔，用来算序列数
	AggBytesFactor   float64       // 聚合点每点字节 ÷ 明细点每点字节
	RetainDetailDays int
	Retain1mDays     int
	Retain5mDays     int
}

// CapacityTier 是一层存储的估算结果。
type CapacityTier struct {
	Key             string
	Name            string
	Interval        time.Duration
	PointsPerSecond float64
	BytesPerPoint   float64
	RetentionDays   int
	RawBytes        float64
	StoredBytes     float64
	CostPerYear     float64
}

// CapacityReport 是三层合起来的账。
type CapacityReport struct {
	Input            CapacityInput
	Series           float64
	Tiers            []CapacityTier
	TotalStoredBytes float64
	TotalCostPerYear float64
	DetailOneYear    CapacityTier // 明细层如果留满一年
	DetailExtraCost  float64      // 明细从 30 天改成一年的增量成本
}

// estimateCapacity 按上面的式子算三层。
func estimateCapacity(in CapacityInput) CapacityReport {
	rep := CapacityReport{Input: in}
	series := in.PointsPerSecond * in.DetailInterval.Seconds()
	rep.Series = series
	aggBytes := in.BytesPerPoint * in.AggBytesFactor

	rep.Tiers = []CapacityTier{
		makeTier("detail", "明细", in.DetailInterval, in.PointsPerSecond, in.BytesPerPoint, in.RetainDetailDays, in),
		makeTier("1m", "1 分钟", time.Minute, series/60, aggBytes, in.Retain1mDays, in),
		makeTier("5m", "5 分钟", 5*time.Minute, series/300, aggBytes, in.Retain5mDays, in),
	}
	for _, t := range rep.Tiers {
		rep.TotalStoredBytes += t.StoredBytes
		rep.TotalCostPerYear += t.CostPerYear
	}

	rep.DetailOneYear = makeTier("detail-365", "明细（留一年）", in.DetailInterval,
		in.PointsPerSecond, in.BytesPerPoint, 365, in)
	rep.DetailExtraCost = rep.DetailOneYear.CostPerYear - rep.Tiers[0].CostPerYear
	return rep
}

func makeTier(key, name string, interval time.Duration, pps, bytesPerPoint float64, days int, in CapacityInput) CapacityTier {
	raw := pps * bytesPerPoint * 86400 * float64(days)
	stored := raw / in.CompressRatio * float64(in.Replicas) * in.WriteAmp
	return CapacityTier{
		Key:             key,
		Name:            name,
		Interval:        interval,
		PointsPerSecond: pps,
		BytesPerPoint:   bytesPerPoint,
		RetentionDays:   days,
		RawBytes:        raw,
		StoredBytes:     stored,
		CostPerYear:     stored / 1e12 * in.PricePerTBYear,
	}
}
