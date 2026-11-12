package main

import (
	"encoding/json"
	"testing"
	"time"
)

// oneAck 造一条挂在某台探针上的确认。
func oneAck(probeID, taskID, metricID string, appliedAt time.Duration) *Ack {
	return &Ack{ProbeID: probeID, TaskID: taskID, MetricID: metricID, Revision: 1, AppliedAt: appliedAt, RevokedAt: -1}
}

func TestBatchJSONContractFields(t *testing.T) {
	b := ReportBatch{
		ProbeID:    "probe-a-01",
		BatchSeq:   7,
		ProducedAt: stamp(11 * time.Minute),
		Points: []Point{{
			ProbeID: "probe-a-01", MetricID: "if_err_rate",
			AckedBatchSeq: 3, AckedTS: stamp(10 * time.Minute),
		}},
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("序列化批次失败：%v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	wantTop := []string{"probe_id", "batch_seq", "produced_at", "points"}
	if len(top) != len(wantTop) {
		t.Fatalf("批次字段应当恰好是 %v，实际 %v", wantTop, keysOf(top))
	}
	for _, k := range wantTop {
		if _, ok := top[k]; !ok {
			t.Fatalf("批次缺字段 %s，实际 %v", k, keysOf(top))
		}
	}

	var pts []map[string]json.RawMessage
	if err := json.Unmarshal(top["points"], &pts); err != nil {
		t.Fatalf("points 不是数组：%v", err)
	}
	wantPoint := []string{"probe_id", "metric_id", "acked_batch_seq", "acked_ts"}
	if len(pts) != 1 || len(pts[0]) != len(wantPoint) {
		t.Fatalf("点位字段应当恰好是 %v，实际 %v", wantPoint, keysOf(pts[0]))
	}
	var seq uint64
	if err := json.Unmarshal(pts[0]["acked_batch_seq"], &seq); err != nil {
		t.Fatalf("acked_batch_seq 必须是数字（uint64）：%v", err)
	}
	var ts string
	if err := json.Unmarshal(pts[0]["acked_ts"], &ts); err != nil {
		t.Fatalf("acked_ts 必须是字符串：%v", err)
	}
	for _, s := range []string{ts, b.ProducedAt} {
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			t.Fatalf("时间戳 %q 不是 RFC3339：%v", s, err)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestBatchSeqMonotonicPerProbe(t *testing.T) {
	cfg, sc := buildDefault(t)
	pull := SimulatePull(cfg, sc.Probes, sc.Changes)
	deliveries, st := MaterializeBatches(cfg, sc.Probes, pull.Apply)
	if len(deliveries) == 0 {
		t.Fatal("应当有批次产出")
	}
	last := map[string]uint64{}
	for _, d := range deliveries {
		seq := last[d.Batch.ProbeID]
		if d.Batch.BatchSeq != seq+1 {
			t.Fatalf("%s 的批次序号应当连续递增：上一条 %d，这条 %d",
				d.Batch.ProbeID, seq, d.Batch.BatchSeq)
		}
		last[d.Batch.ProbeID] = d.Batch.BatchSeq
		if len(d.Batch.Points) == 0 {
			t.Fatalf("%s/%d 是空批次，空批次不该上报", d.Batch.ProbeID, d.Batch.BatchSeq)
		}
		for _, p := range d.Batch.Points {
			if p.AckedBatchSeq == 0 || p.AckedBatchSeq > d.Batch.BatchSeq {
				t.Fatalf("%s 的 acked_batch_seq=%d 不在 [1, %d] 内",
					d.Batch.ProbeID, p.AckedBatchSeq, d.Batch.BatchSeq)
			}
		}
	}
	if st.Produced != st.Delivered+st.Dropped {
		t.Fatalf("生产 %d 应当等于投递 %d + 丢弃 %d", st.Produced, st.Delivered, st.Dropped)
	}
}

func TestRepeatedAckKeepsOriginalBatchSeq(t *testing.T) {
	cfg := testConfig()
	p := oneProbe("p-1")
	acks := []*Ack{oneAck("p-1", "t-1", "if_err_rate", 30*time.Second)}
	deliveries, _ := MaterializeProbe(cfg, p, acks)
	if len(deliveries) < 2 {
		t.Fatalf("10 分钟 / 1 分钟应当有 10 个批次，实际 %d", len(deliveries))
	}
	first := deliveries[0].Batch
	for _, d := range deliveries {
		pt := d.Batch.Points[0]
		if pt.AckedBatchSeq != first.Points[0].AckedBatchSeq {
			t.Fatalf("同一条确认在后续批次里应当保持 acked_batch_seq=%d，实际 %d（批次 %d）",
				first.Points[0].AckedBatchSeq, pt.AckedBatchSeq, d.Batch.BatchSeq)
		}
		if pt.AckedTS != first.Points[0].AckedTS {
			t.Fatalf("同一条确认的 acked_ts 应当不变，实际 %s vs %s", pt.AckedTS, first.Points[0].AckedTS)
		}
	}
}

func TestBufferedBatchesWithinMaxReportDelay(t *testing.T) {
	cfg := testConfig()
	p := oneProbe("p-1", Window{From: 3 * time.Minute, To: 5 * time.Minute})
	acks := []*Ack{oneAck("p-1", "t-1", "if_err_rate", 30*time.Second)}
	deliveries, st := MaterializeProbe(cfg, p, acks)

	var late int
	for _, d := range deliveries {
		if d.Lateness() > 0 {
			late++
			if d.Lateness() > cfg.MaxReportDelay {
				t.Fatalf("补报延迟 %s 超过上限 %s 却仍然投递了", dur(d.Lateness()), dur(cfg.MaxReportDelay))
			}
			if d.ArrivalAt != 5*time.Minute {
				t.Fatalf("离线期间的批次应当在 5m0s 一起补报，实际 %s", dur(d.ArrivalAt))
			}
		}
	}
	if late == 0 {
		t.Fatal("离线窗口覆盖了两轮上报，应当有迟到批次")
	}
	if st.Dropped != 0 {
		t.Fatalf("补报延迟在窗口内不该丢批次，实际丢 %d 批", st.Dropped)
	}
	if st.MaxLateness <= 0 {
		t.Fatal("最大补报延迟应当被记录下来")
	}
}

func TestBatchesBeyondWindowDropped(t *testing.T) {
	cfg := testConfig()
	cfg.MaxReportDelay = 30 * time.Second
	p := oneProbe("p-1", Window{From: 3 * time.Minute, To: 5 * time.Minute})
	acks := []*Ack{oneAck("p-1", "t-1", "if_err_rate", 30*time.Second)}
	deliveries, st := MaterializeProbe(cfg, p, acks)

	for _, d := range deliveries {
		if d.Lateness() > cfg.MaxReportDelay {
			t.Fatalf("补报延迟 %s 超过上限却投递了", dur(d.Lateness()))
		}
	}
	if st.Dropped != 2 {
		t.Fatalf("3m 与 4m 两批都超过 30s 上限，应当丢 2 批，实际 %d", st.Dropped)
	}
	if st.OverWindow != 2 {
		t.Fatalf("两批丢弃都应当是「超过补报窗口」，实际 OverWindow=%d", st.OverWindow)
	}
}

func TestDecommissionedProbeStopsProducing(t *testing.T) {
	cfg := testConfig()
	p := oneProbe("p-1")
	p.DecommissionedAt = 3 * time.Minute
	acks := []*Ack{oneAck("p-1", "t-1", "if_err_rate", 30*time.Second)}
	deliveries, st := MaterializeProbe(cfg, p, acks)
	if st.Produced != 2 {
		t.Fatalf("3 分钟退役的探针只能产出 2 批（1m、2m），实际 %d", st.Produced)
	}
	if len(deliveries) != 2 {
		t.Fatalf("本地缓冲跟着探针一起没了，不该有补报，实际投递 %d 批", len(deliveries))
	}
}

func TestRevokedAckStopsReporting(t *testing.T) {
	cfg := testConfig()
	p := oneProbe("p-1")
	ack := oneAck("p-1", "t-1", "if_err_rate", 30*time.Second)
	ack.RevokedAt = 4 * time.Minute
	deliveries, _ := MaterializeProbe(cfg, p, []*Ack{ack})
	for _, d := range deliveries {
		if d.Batch.ProducedOffset() >= 4*time.Minute {
			t.Fatalf("任务在 4m0s 被收回，%s 的批次里不该还有它", dur(d.Batch.ProducedOffset()))
		}
	}
	// 1m、2m、3m 三批；4m0s 那批已经到了撤销之后，不该再有这条任务。
	if len(deliveries) != 3 {
		t.Fatalf("撤销前应当有 3 批，实际 %d", len(deliveries))
	}
}

func TestDuplicateDispatchDoubleCountsMetric(t *testing.T) {
	cfg, sc := buildDefault(t)
	if sc.Truth.DuplicateDispatches == 0 {
		t.Fatal("样本里应当至少注入一次重复下发，否则这条测试是空的")
	}
	pull := SimulatePull(cfg, sc.Probes, sc.Changes)
	deliveries, _ := MaterializeBatches(cfg, sc.Probes, pull.Apply)

	// metric_id 唯一标识一条时序，正常情况下一台探针报它。两台探针在同一批里报同一条，
	// 就是重复下发；管道按 (probe_id, batch_seq, metric_id) 去重，压不住跨探针的这一份。
	// 换点也会让两台探针先后报同一条，那不是重复：批次不重叠，按 produced_at 一眼分得清。
	type tickMetric struct{ tick, metric string }
	atTick := map[tickMetric]map[string]bool{}
	for _, d := range deliveries {
		for _, p := range d.Batch.Points {
			k := tickMetric{d.Batch.ProducedAt, p.MetricID}
			if atTick[k] == nil {
				atTick[k] = map[string]bool{}
			}
			atTick[k][p.ProbeID] = true
		}
	}
	doubleMetrics := map[string]bool{}
	for k, probes := range atTick {
		if len(probes) > 1 {
			doubleMetrics[k.metric] = true
		}
	}
	double := len(doubleMetrics)
	if double != len(sc.Truth.DuplicateTasks) {
		t.Fatalf("注入 %d 起重复下发，批次里应当有 %d 个 metric 被两台探针同时上报，实际 %d",
			sc.Truth.DuplicateDispatches, len(sc.Truth.DuplicateTasks), double)
	}
}

func TestCheckBatchScale(t *testing.T) {
	cfg := testConfig()
	cfg.Probes = batchMaterializeLimit + 1
	if err := CheckBatchScale(cfg); err == nil {
		t.Fatal("超过护栏的规模应当被拒绝，而不是跑到一半 OOM")
	}
	cfg.Probes = batchMaterializeLimit
	if err := CheckBatchScale(cfg); err != nil {
		t.Fatalf("护栏边界应当放行：%v", err)
	}
}
