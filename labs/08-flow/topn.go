package main

import (
	"math"
	"sort"
)

// TopEntry 是 TopN 上的一行。
//
// Samples 的含义随源变化：sFlow 是抽到的样本数，NetFlow 是 exporter 报的记录条数。
// 两者的误差含义也完全不同，所以分成两个源各出一张表，不做合并统计。
type TopEntry struct {
	Key     FlowKey `json:"flow"`
	Bytes   int64   `json:"bytes"`
	Packets int64   `json:"packets"`
	Samples int64   `json:"samples"`
	// Share 是这一条流在同一个源内的字节占比。
	Share float64 `json:"share"`
	// RelErr 是估算值的相对标准误差上界，只有 sFlow 有意义。
	// 抽到 c 个样本时约 1/√c：c=4 就是 ±50%，c=100 是 ±10%。
	RelErr float64 `json:"rel_err,omitempty"`
}

// SourceStat 是一个数据源的一份 TopN 统计。
type SourceStat struct {
	// Name 是产物里的机器名：sflow / netflow / merged。
	Name string `json:"source"`
	// Label 是一句话口径，打表头时带上。口径写错，数字全对也没用。
	Label   string     `json:"label"`
	Entries []TopEntry `json:"top"`
	Bytes   int64      `json:"bytes"`
	Packets int64      `json:"packets"`
	// Units 是参与统计的可信单元数：sFlow 是样本数，NetFlow 是记录条数。
	Units int64 `json:"units"`
	// DroppedUnits 是被判不可信而丢掉的单元数。它不参与字节统计，
	// 只用来算覆盖率——不解析就没有字节数，所以覆盖率只能按条数算。
	DroppedUnits      int64 `json:"dropped_units"`
	UntrustedSegments int   `json:"untrusted_segments,omitempty"`
	Segments          int   `json:"segments,omitempty"`
	// Coverage 是可信单元占全部单元的比值。
	Coverage float64 `json:"coverage"`
	// DropReasons 记每个丢弃原因各有多少条，报告要逐条列。
	DropReasons map[string]int64 `json:"drop_reasons,omitempty"`
}

// sFlow 侧的两个丢弃原因。采样率缺失和报文头长度为 0 都意味着「这条样本放大不出来」，
// 放大不出来就不能进字节统计，否则一个 0 会被乘成 0，看起来像「这条流没流量」。
const (
	reasonRateMissing  = "sampling_rate_missing"
	reasonBytesMissing = "header_bytes_missing"
)

var sflowDropZH = map[string]string{
	reasonRateMissing:  "采样率缺失或为 0",
	reasonBytesMissing: "报文头长度为 0",
}

func coverage(kept, dropped int64) float64 {
	total := kept + dropped
	if total == 0 {
		return 1
	}
	return float64(kept) / float64(total)
}

// relErr 是抽样估计的相对标准误差：抽到 c 个样本时约 1/√c。
// c=1 就是 ±100%，c=4 是 ±50%。这条式子解释了为什么 1:1000 下 TopN 的
// 第 4、5 名不可信：它们往往只有一两个样本。
func relErr(samples int64) float64 {
	if samples <= 0 {
		return 0
	}
	return 1 / math.Sqrt(float64(samples))
}

// topN 排序：字节多的在前；字节相同按 key 的字符串序，保证同一份样本每次输出一致。
// 平局必排序，否则 TopN 的尾巴会在两次跑之间跳来跳去，看着像数据在变。
func sortEntries(entries []TopEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Bytes != entries[j].Bytes {
			return entries[i].Bytes > entries[j].Bytes
		}
		return entries[i].Key.String() < entries[j].Key.String()
	})
}

// AggregateSFlow 把报文头样本按采样率放大成字节数，再按五元组做 TopN。
//
// 一个样本放大 1/rate：rate=1000 时每个样本代表 1000 个报文。
// 这是估算，不是计数——误差约 1/√c，c 是这条流抽到的样本数。
func AggregateSFlow(dgs []SFlowDatagram, topN int) SourceStat {
	st := SourceStat{
		Name:        "sflow",
		Label:       "报文头样本 × 采样率（估算值）",
		DropReasons: map[string]int64{},
	}
	agg := make(map[FlowKey]*TopEntry)
	var kept int64

	for _, d := range dgs {
		for _, s := range d.Samples {
			switch {
			case s.SamplingRate <= 0:
				st.DroppedUnits++
				st.DropReasons[reasonRateMissing]++
				continue
			case s.Header.Bytes <= 0:
				st.DroppedUnits++
				st.DropReasons[reasonBytesMissing]++
				continue
			}
			kept++
			key := makeKey(s.Header.SrcIP, s.Header.DstIP, s.Header.Proto, s.Header.SrcPort, s.Header.DstPort)
			e, ok := agg[key]
			if !ok {
				e = &TopEntry{Key: key}
				agg[key] = e
			}
			// 一个样本代表 rate 个报文，平均每个 rate 字节数是「样本长度 × rate」。
			e.Bytes += s.Header.Bytes * int64(s.SamplingRate)
			e.Packets += int64(s.SamplingRate)
			e.Samples++
		}
	}

	entries := make([]TopEntry, 0, len(agg))
	for _, e := range agg {
		st.Bytes += e.Bytes
		st.Packets += e.Packets
		entries = append(entries, *e)
	}
	sortEntries(entries)
	for i := range entries {
		entries[i].Share = share(entries[i].Bytes, st.Bytes)
		entries[i].RelErr = relErr(entries[i].Samples)
	}

	st.Units = kept
	st.Coverage = coverage(kept, st.DroppedUnits)
	st.Entries = truncate(entries, topN)
	return st
}

// AggregateNetFlow 汇总解码出来的流记录。这里没有采样率这个变量：
// 字节数与包数是 exporter 聚合好的定量，直接相加。
func AggregateNetFlow(res DecodeResult, topN int) SourceStat {
	st := SourceStat{
		Name:              "netflow",
		Label:             "流记录聚合值（exporter 侧计数）",
		UntrustedSegments: res.DroppedSegments,
		Segments:          len(res.Segments),
		DropReasons:       map[string]int64{},
	}
	agg := make(map[FlowKey]*TopEntry)
	for _, rec := range res.Records {
		e, ok := agg[rec.Key]
		if !ok {
			e = &TopEntry{Key: rec.Key}
			agg[rec.Key] = e
		}
		e.Bytes += rec.Octets
		e.Packets += rec.Packets
		e.Samples++
	}

	entries := make([]TopEntry, 0, len(agg))
	for _, e := range agg {
		st.Bytes += e.Bytes
		st.Packets += e.Packets
		entries = append(entries, *e)
	}
	sortEntries(entries)
	for i := range entries {
		entries[i].Share = share(entries[i].Bytes, st.Bytes)
	}

	for _, seg := range res.Segments {
		if !seg.Trusted {
			st.DropReasons[seg.Reason] += int64(seg.Dropped)
		}
	}
	st.Units = int64(trustedRecordCount(res.Segments))
	st.DroppedUnits = int64(droppedRecordCount(res.Segments))
	st.Coverage = coverage(st.Units, st.DroppedUnits)
	st.Entries = truncate(entries, topN)
	return st
}

// MergeStats 把两个源的 TopN 直接相加。这个函数存在的唯一目的是演示它错在哪：
// 同一份流量既被 sFlow 抽到过，也被 NetFlow 聚合过，相加就是双计。
func MergeStats(sflow, netflow SourceStat, topN int) SourceStat {
	st := SourceStat{
		Name:              "merged",
		Label:             "两个源直接相加（演示：同一条流被算了两遍）",
		UntrustedSegments: sflow.UntrustedSegments + netflow.UntrustedSegments,
		Segments:          sflow.Segments + netflow.Segments,
		DropReasons:       map[string]int64{},
	}
	agg := make(map[FlowKey]*TopEntry)
	for _, src := range []SourceStat{sflow, netflow} {
		for _, e := range src.Entries {
			v, ok := agg[e.Key]
			if !ok {
				v = &TopEntry{Key: e.Key}
				agg[e.Key] = v
			}
			v.Bytes += e.Bytes
			v.Packets += e.Packets
			v.Samples += e.Samples
		}
	}
	entries := make([]TopEntry, 0, len(agg))
	for _, e := range agg {
		st.Bytes += e.Bytes
		st.Packets += e.Packets
		entries = append(entries, *e)
	}
	sortEntries(entries)
	for i := range entries {
		entries[i].Share = share(entries[i].Bytes, st.Bytes)
	}
	st.Entries = truncate(entries, topN)
	return st
}

// share 是占比。总量为 0 时返回 0，不返回 NaN——NaN 打印出来比没有数字更糟。
func share(part, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// truncate 只留前 n 条。
func truncate(entries []TopEntry, n int) []TopEntry {
	if n >= len(entries) {
		return entries
	}
	return entries[:n]
}
