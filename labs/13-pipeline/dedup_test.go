package main

import "testing"

// aggregate 是「把所有点加一遍」的粗聚合，用来判重放前后是否一致。
type aggregate struct {
	Sum   float64
	Count int
}

func aggregateOf(recs []Record) aggregate {
	var a aggregate
	for _, r := range recs {
		for _, p := range r.Points {
			a.Sum += p.Value
			a.Count++
		}
	}
	return a
}

func rec(probe string, seq uint64, probeIdx int) Record {
	return Record{
		ProbeID: probe, BatchSeq: seq,
		ProducedAt: dateTime(2025, 3, 11, 9, 0, 0, 0),
		Points: []Datum{
			{ProbeID: probe, AckedBatchSeq: 1, AckedTS: dateTime(2025, 3, 11, 9, 0, 0, 0),
				MetricID: "rtt_p50_us", Value: float64(100 + probeIdx)},
			{ProbeID: probe, AckedBatchSeq: 1, AckedTS: dateTime(2025, 3, 11, 9, 0, 0, 0),
				MetricID: "jitter_us", Value: float64(10 + probeIdx)},
		},
	}
}

// 幂等重放：同一包数据再喂一遍，聚合值必须一模一样。
func TestDedupReplayKeepsAggregate(t *testing.T) {
	recs, _, _ := mustLoadSample(t)
	single := dedupAll(recs)
	twice := make([]Record, 0, 2*len(recs))
	twice = append(twice, recs...)
	twice = append(twice, recs...)
	replayed := dedupAll(twice)

	if a, b := aggregateOf(single.Records), aggregateOf(replayed.Records); a != b {
		t.Fatalf("重放后聚合变了：%+v ≠ %+v", a, b)
	}
	if replayed.Stats.PointsAccepted != single.Stats.PointsAccepted {
		t.Fatalf("重放后接受点数变了：%d ≠ %d",
			replayed.Stats.PointsAccepted, single.Stats.PointsAccepted)
	}
	// 第二遍进来的点，一个都不该被接受：第一遍接受的每个点都被幂等键挡住，
	// 第一遍判冲突的点第二遍照样冲突，不会凭空变成重复。
	// 第二遍里凡是没判冲突的点，都会被第一道闸（幂等键）挡住：第一遍按确认键压掉的
	// 那几条重复上报，在第二遍里先撞上幂等键，计入重复点数而不是重复上报数。
	wantDup := single.Stats.PointsDuplicated + (single.Stats.PointsIn - single.Stats.PointsConflicted)
	if replayed.Stats.PointsDuplicated != wantDup {
		t.Fatalf("重放时幂等键命中 %d，期望 %d", replayed.Stats.PointsDuplicated, wantDup)
	}
	// 冲突批次重放一遍还是冲突：冲突数正好翻倍，但它们一个点也没被接受。
	if replayed.Stats.PointsConflicted != 2*single.Stats.PointsConflicted {
		t.Fatalf("冲突点数 %d，期望是第一遍 %d 的两倍",
			replayed.Stats.PointsConflicted, single.Stats.PointsConflicted)
	}
}

func TestDedupReplayHitsEveryPoint(t *testing.T) {
	d := NewDeduper()
	r := rec("p1", 5, 0)
	if _, note := d.Accept(r); note.Accepted != 2 || note.Outcome != outcomeNew {
		t.Fatalf("首次应新收 2 点，实际 %+v", note)
	}
	if _, note := d.Accept(r); note.Accepted != 0 || note.Duplicated != 2 || note.Outcome != outcomeReplay {
		t.Fatalf("重放应全部命中幂等键，实际 %+v", note)
	}
}

// 部分写入：先到半份，后到整份，只补缺的那几个点，不重复计数。
func TestDedupPartialWriteCompletes(t *testing.T) {
	d := NewDeduper()
	full := rec("p1", 9, 0)

	half := full
	half.Points = full.Points[:1]
	if _, note := d.Accept(half); note.Accepted != 1 || note.Outcome != outcomeNew {
		t.Fatalf("半份应是新批次收 1 点，实际 %+v", note)
	}
	_, note := d.Accept(full)
	if note.Outcome != outcomeComplete {
		t.Fatalf("整份应判为部分补全，实际 %+v", note)
	}
	if note.Accepted != 1 || note.Duplicated != 1 {
		t.Fatalf("补全应只收 1 点、命中 1 点，实际收 %d 重 %d", note.Accepted, note.Duplicated)
	}

	// 三次送同一份，一个点都不能再进。
	if _, again := d.Accept(full); again.Accepted != 0 {
		t.Fatalf("补全之后再送一遍，又收了 %d 个点", again.Accepted)
	}
}

// 冲突：同一个 (probe_id, batch_seq) 两次值不同，整批拒，不许以后到的为准。
func TestDedupConflictRejectsWholeBatch(t *testing.T) {
	d := NewDeduper()
	first := rec("p1", 11, 0)
	if _, note := d.Accept(first); note.Accepted != 2 {
		t.Fatalf("首次应收 2 点，实际 %+v", note)
	}

	other := rec("p1", 11, 5) // 同样的批次号，值不同
	kept, note := d.Accept(other)
	if note.Outcome != outcomeConflict {
		t.Fatalf("该判冲突，实际 %+v", note)
	}
	if len(kept.Points) != 0 || note.Accepted != 0 {
		t.Fatalf("冲突批次不该收任何点，实际收 %d", note.Accepted)
	}
	if note.Conflicted != 2 {
		t.Fatalf("冲突点数应是 2，实际 %d", note.Conflicted)
	}
}

// 幂等键到点位粒度：同批次的两个 metric 里只有一个被写过，重放时另一个能补上。
func TestDedupKeyIsPerPoint(t *testing.T) {
	d := NewDeduper()
	r := rec("p1", 3, 0)
	first := r
	first.Points = r.Points[:1]
	if _, note := d.Accept(first); note.Accepted != 1 {
		t.Fatalf("先收 1 点，实际 %+v", note)
	}
	_, note := d.Accept(r)
	if note.Accepted != 1 || note.Duplicated != 1 {
		t.Fatalf("整批粒度只能全收或全丢，点位粒度才能补 1 点：%+v", note)
	}
}

// 批次号回退：探针重启后从 1 重数。撞上老号判冲突，新号收下并标记。
func TestDedupSeqRegressionIsMarked(t *testing.T) {
	d := NewDeduper()
	for seq := uint64(1); seq <= 40; seq++ {
		d.Accept(rec("p1", seq, 0))
	}

	fresh := rec("p1", 41, 3)
	if _, note := d.Accept(fresh); note.Regressed {
		t.Fatalf("正常递增不该标回退：seq=41，max=%d", d.maxSeq["p1"])
	}

	// 重启后回到 1：老号，内容不同 → 冲突，且标记回退。
	_, note := d.Accept(rec("p1", 1, 7))
	if note.Outcome != outcomeConflict || !note.Regressed {
		t.Fatalf("重启撞老号应判冲突并标记回退，实际 %+v", note)
	}
}

// 乱序不算回退：批次号前后颠倒几档是常态，阈值以内的不值得报警。
func TestDedupToleratesReorder(t *testing.T) {
	d := NewDeduper()
	d.Accept(rec("p1", 20, 0))
	if _, note := d.Accept(rec("p1", 20-seqReorderAllowance+1, 1)); note.Regressed {
		t.Fatalf("回退 %d 档以内不该标回退：%+v", seqReorderAllowance-1, note)
	}
	if _, note := d.Accept(rec("p1", 1, 2)); !note.Regressed {
		t.Fatalf("从 20 掉回 1 应标回退：%+v", note)
	}
}

// 第二道闸：同一条确认在后续批次里原样重报，信封的 batch_seq 变了，
// 幂等键压不住，确认键 (probe_id, metric_id, acked_batch_seq) 才压得住。
func TestDedupRepeatedReportUsesAckKey(t *testing.T) {
	d := NewDeduper()
	first := rec("p1", 10, 0)
	first.Points[0].AckedBatchSeq = 10
	first.Points[1].AckedBatchSeq = 10
	if _, note := d.Accept(first); note.Accepted != 2 || note.Repeated != 0 {
		t.Fatalf("首批应收 2 点，实际 %+v", note)
	}

	// 下一批把自己的两项换成上一批原样重报的那两项
	next := rec("p1", 11, 0)
	next.Points[0] = first.Points[0]
	next.Points[1].AckedBatchSeq = 11
	_, note := d.Accept(next)
	if note.Outcome != outcomeNew {
		t.Fatalf("信封序号变了就是新批次，实际 %+v", note)
	}
	if note.Repeated != 1 || note.Accepted != 1 {
		t.Fatalf("应压掉 1 个重复上报、收 1 个新点，实际 %+v", note)
	}
	if note.Duplicated != 0 {
		t.Fatalf("这一个不该算幂等键命中，它是确认键挡下的：%+v", note)
	}
}

// 同一个批次里同一个 metric 出现两次：幂等键只能留下先到的那个值，第二个被静默压掉。
// 这就是校验层要整批拒掉它的原因——压掉是错的，静默是更错的。
func TestDedupDuplicateMetricInBatchKeepsFirst(t *testing.T) {
	d := NewDeduper()
	r := rec("p1", 1, 0) // rtt_p50_us=100, jitter_us=10
	second := r.Points[0]
	second.Value = 999
	r.Points = append(r.Points, second)

	kept, note := d.Accept(r)
	if note.Accepted != 2 || note.Duplicated != 1 {
		t.Fatalf("应留下 2 个点、压掉 1 个：%+v", note)
	}
	if len(kept.Points) != 2 {
		t.Fatalf("留下的点位数 %d ≠ 2", len(kept.Points))
	}
	if kept.Points[0].Value != 100 {
		t.Fatalf("留下的应是先到的值 100，实际 %v", kept.Points[0].Value)
	}
}

func TestDedupStatsAccountForEveryPoint(t *testing.T) {
	recs, _, _ := mustLoadSample(t)
	d := dedupAll(recs)
	s := d.Stats
	if s.PointsIn != s.PointsAccepted+s.PointsDuplicated+s.PointsRepeated+s.PointsConflicted {
		t.Fatalf("点数不守恒：进 %d，收 %d + 重放 %d + 重报 %d + 冲突 %d",
			s.PointsIn, s.PointsAccepted, s.PointsDuplicated, s.PointsRepeated, s.PointsConflicted)
	}
	if s.BatchesIn != s.BatchesAccepted+s.ReplayBatches+s.ConflictBatches {
		t.Fatalf("批次数不守恒：进 %d，收 %d + 重放 %d + 冲突 %d",
			s.BatchesIn, s.BatchesAccepted, s.ReplayBatches, s.ConflictBatches)
	}
	if countPoints(d.Records) != s.PointsAccepted {
		t.Fatalf("留下的点位数 %d ≠ 统计 %d", countPoints(d.Records), s.PointsAccepted)
	}
	if s.PointsRepeated == 0 {
		t.Fatal("样本里有跨批次重复上报的确认，应该被确认键压掉并统计出来")
	}
	if s.RestartConflicts == 0 {
		t.Fatal("样本里有探针重启撞老号，应该统计出来")
	}
}
