package main

import (
	"fmt"
	"sort"
	"time"
)

// Ack 是探针本地的一条确认记录。RevokedAt < 0 表示任务还在这台探针上。
type Ack struct {
	ProbeID   string
	TaskID    string
	MetricID  string
	Revision  int
	AppliedAt time.Duration
	RevokedAt time.Duration

	// reportedSeq / reportedAt 在生成批次时填：这条确认第一次被上报时所在批次的序号与时刻。
	reportedSeq uint64
	reportedAt  time.Duration
}

// Delivery 是一个批次的投递记录。ArrivalAt 是它真正到达采集端的时刻，
// 与 ProducedAt 的差就是补报延迟。
type Delivery struct {
	Batch     ReportBatch
	ArrivalAt time.Duration
}

// Lateness 是补报延迟：到达时刻减去批次生成时刻。
func (d Delivery) Lateness() time.Duration { return d.ArrivalAt - d.Batch.ProducedOffset() }

// ProducedOffset 把批次上的 produced_at 解析回仿真偏移。解析失败返回 0 让调用方看见偏差。
func (b ReportBatch) ProducedOffset() time.Duration {
	t, err := time.Parse(time.RFC3339Nano, b.ProducedAt)
	if err != nil {
		return 0
	}
	return t.Sub(baseTime)
}

// BatchStats 是落盘时的账。
type BatchStats struct {
	Produced    int
	Delivered   int
	Dropped     int // 超过最大补报延迟，或仿真结束仍在缓冲
	OverWindow  int // 其中因为超过最大补报延迟丢掉的
	Points      int
	MaxLateness time.Duration
	LateBatches int // 补报延迟超过一个上报周期的批次
}

// BuildAcks 把应用记录压成每台探针的确认表。
//
// 收活记录（RoleRevoke）给对应的确认打上撤销时刻：从那一刻起这台探针不再上报这条任务。
// 重复下发那份额外的派活没有撤销记录——这正是它的形态，旧点会一直报下去。
func BuildAcks(apps []Application) map[string][]*Ack {
	byProbeTask := make(map[string]*Ack, len(apps))
	order := make([]string, 0, len(apps))
	for _, a := range apps {
		k := a.ProbeID + "/" + a.TaskID
		switch a.Role {
		case RoleAssign:
			if prev, ok := byProbeTask[k]; ok && prev.Revision >= a.Revision && prev.AppliedAt <= a.AppliedAt {
				continue
			}
			byProbeTask[k] = &Ack{
				ProbeID: a.ProbeID, TaskID: a.TaskID, MetricID: a.MetricID,
				Revision: a.Revision, AppliedAt: a.AppliedAt, RevokedAt: -1,
			}
			order = append(order, k)
		case RoleRevoke:
			if ack, ok := byProbeTask[k]; ok {
				ack.RevokedAt = a.AppliedAt
			}
		}
	}
	out := make(map[string][]*Ack)
	seen := make(map[string]bool, len(order))
	for _, k := range order {
		ack, ok := byProbeTask[k]
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		out[ack.ProbeID] = append(out[ack.ProbeID], ack)
	}
	for _, acks := range out {
		sort.SliceStable(acks, func(i, j int) bool {
			if acks[i].AppliedAt != acks[j].AppliedAt {
				return acks[i].AppliedAt < acks[j].AppliedAt
			}
			return acks[i].TaskID < acks[j].TaskID
		})
	}
	return out
}

// MaterializeProbe 生成一台探针的全部上报批次并算出投递时刻。
//
// 探针离线期间任务照跑、确认照记，只是发不出去：批次堆在本地缓冲里，
// 恢复之后一起补报。补报有个上限，超过最大补报延迟的批次直接丢——
// 丢了就是缺口，晚到的才是「迟到数据」。这两个词在 33 课里是两个处理分支。
func MaterializeProbe(cfg Config, p Probe, acks []*Ack) (deliveries []Delivery, st BatchStats) {
	var seq uint64
	for tick := cfg.ReportInterval; tick <= cfg.Duration; tick += cfg.ReportInterval {
		if p.DecommissionedAt >= 0 && tick >= p.DecommissionedAt {
			// 探针死了，本地缓冲跟着一起没了。
			break
		}
		points := make([]Point, 0, len(acks))
		for _, a := range acks {
			if a.AppliedAt > tick {
				continue
			}
			if a.RevokedAt >= 0 && tick >= a.RevokedAt {
				continue
			}
			if a.reportedSeq == 0 {
				// 这一批是这个 revision 第一次上报，定下它的批次号；后面每批都用同一个。
				a.reportedSeq = seq + 1
				a.reportedAt = tick
			}
			points = append(points, Point{
				ProbeID:       a.ProbeID,
				MetricID:      a.MetricID,
				AckedBatchSeq: a.reportedSeq,
				AckedTS:       stamp(a.reportedAt),
			})
		}
		if len(points) == 0 {
			continue
		}
		seq++
		batch := ReportBatch{
			ProbeID:    p.ProbeID,
			BatchSeq:   seq,
			ProducedAt: stamp(tick),
			Points:     points,
		}
		st.Produced++

		arrival := tick
		if !p.online(tick) {
			w, ok := offlineWindowAt(p, tick)
			if !ok {
				st.Dropped++
				continue
			}
			arrival = w.To
		}
		late := arrival - tick
		if late > cfg.MaxReportDelay || arrival > cfg.Duration {
			st.Dropped++
			if late > cfg.MaxReportDelay {
				st.OverWindow++
			}
			continue
		}
		if late > 0 {
			if late > st.MaxLateness {
				st.MaxLateness = late
			}
			if late > cfg.ReportInterval {
				st.LateBatches++
			}
		}
		st.Delivered++
		st.Points += len(points)
		deliveries = append(deliveries, Delivery{Batch: batch, ArrivalAt: arrival})
	}
	return deliveries, st
}

// offlineWindowAt 找覆盖 t 的离线窗口。
func offlineWindowAt(p Probe, t time.Duration) (Window, bool) {
	for _, w := range p.Offline {
		if t >= w.From && t < w.To {
			return w, true
		}
	}
	return Window{}, false
}

// MaterializeBatches 生成全部探针的批次，按到达时刻排序——这份顺序就是 13-pipeline 的输入顺序。
func MaterializeBatches(cfg Config, probes []Probe, apps []Application) ([]Delivery, BatchStats) {
	acks := BuildAcks(apps)
	all := make([]Delivery, 0, len(probes)*int(cfg.Duration/cfg.ReportInterval))
	var st BatchStats
	for _, p := range probes {
		d, s := MaterializeProbe(cfg, p, acks[p.ProbeID])
		all = append(all, d...)
		st.Produced += s.Produced
		st.Delivered += s.Delivered
		st.Dropped += s.Dropped
		st.OverWindow += s.OverWindow
		st.Points += s.Points
		if s.MaxLateness > st.MaxLateness {
			st.MaxLateness = s.MaxLateness
		}
		st.LateBatches += s.LateBatches
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].ArrivalAt != all[j].ArrivalAt {
			return all[i].ArrivalAt < all[j].ArrivalAt
		}
		if all[i].Batch.ProbeID != all[j].Batch.ProbeID {
			return all[i].Batch.ProbeID < all[j].Batch.ProbeID
		}
		return all[i].Batch.BatchSeq < all[j].Batch.BatchSeq
	})
	return all, st
}

// batchMaterializeLimit 是一个护栏：一个批次至少占几百字节，十万探针 × 十轮就是几百万行，
// 那不是本实验要量的事，直接拒绝比跑到一半 OOM 好。
const batchMaterializeLimit = 2000

// CheckBatchScale 在落批次之前先挡住注定跑不完的规模。
func CheckBatchScale(cfg Config) error {
	if cfg.Probes > batchMaterializeLimit {
		return fmt.Errorf("探针 %d 台超过落批次的护栏 %d：十万探针只用 -out \"\" 算量，不落 JSONL",
			cfg.Probes, batchMaterializeLimit)
	}
	return nil
}
