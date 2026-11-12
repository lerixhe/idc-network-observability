//go:build ignore

// 样本生成器。跑法：
//
//	cd labs/13-pipeline && go run samples/gen.go
//
// 它写 samples/batches.jsonl，一棵确定性生成的脏样本：迟到、重复、部分写入、
// 冲突、批次号回绕、坏时间戳、坏 JSON。种子固定，跑几次结果一样。
//
// 这里自己定义了一遍结构体，字段名必须与 ../model.go 完全一致。
// 不共用是因为这个文件用 //go:build ignore 排除在模块之外，不让生成器混进实验二进制。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// 字段顺序与 ../model.go 的 Point 一致，也与 12-task 的落盘格式一致。
type genPoint struct {
	ProbeID       string  `json:"probe_id"`
	MetricID      string  `json:"metric_id"`
	AckedBatchSeq uint64  `json:"acked_batch_seq"`
	AckedTS       string  `json:"acked_ts"`
	Value         float64 `json:"value"`
}

type genBatch struct {
	ProbeID    string     `json:"probe_id"`
	BatchSeq   uint64     `json:"batch_seq"`
	ProducedAt string     `json:"produced_at"`
	Points     []genPoint `json:"points"`
}

var (
	probes  = []string{"probe-sh-a1-01", "probe-sh-a1-02", "probe-bj-b2-01"}
	metrics = []string{"rtt_p50_us", "rtt_p99_us", "jitter_us", "loss_pct", "probes_sent", "probes_lost"}
	// 三个探针的 RTT 基准不同：上海同城约 180µs，北京到上海约 640µs。
	rttBase = []float64{180, 260, 640}

	baseSlot = time.Date(2025, 3, 11, 9, 0, 0, 0, time.UTC)
	slotGap  = 15 * time.Second
	slots    = 120 // 每个探针 120 批，覆盖 30 分钟事件时间
)

// item 是待落盘的一行。frozen 的行不参与乱序对调：
// 部分写入那一对必须保持「先半份、后整份」的到达顺序。
type item struct {
	batch  *genBatch
	mutate func(*genBatch)
	raw    string // 直接写入的坏行，优先于 batch
	at     time.Time
	frozen bool
}

func main() {
	items := build()

	sort.SliceStable(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
	shuffle(items)

	f, err := os.Create("samples/batches.jsonl")
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	points := 0
	for _, it := range items {
		if it.raw != "" {
			fmt.Fprintln(f, it.raw)
			continue
		}
		b := *it.batch
		// 点位切片必须复制：mutate 改的是元素，不复制会污染原始批次。
		b.Points = make([]genPoint, len(it.batch.Points))
		copy(b.Points, it.batch.Points)
		if it.mutate != nil {
			it.mutate(&b)
		}
		points += len(b.Points)
		if err := enc.Encode(b); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("写出 samples/batches.jsonl：%d 行 / %d 个点位\n", len(items), points)
}

func build() []item {
	rng := &lcg{s: 20250311}
	b := &builder{}

	base := make(map[string][]*genBatch, len(probes))
	for pi, probe := range probes {
		for i := 0; i < slots; i++ {
			acked := baseSlot.Add(time.Duration(i) * slotGap)
			gb := &genBatch{
				ProbeID:    probe,
				BatchSeq:   uint64(i + 1),
				ProducedAt: acked.Format(time.RFC3339),
				Points:     makePoints(probe, acked, uint64(i+1), pi, i, rng),
			}
			base[probe] = append(base[probe], gb)
			b.add(item{batch: gb, at: acked})
		}
	}

	// 迟到 95~111 秒：网络抖动后批量补报。窗口 60 秒关不上，1 分钟水位线下会丢。
	for i, seq := range []int{20, 21, 22, 23, 24} {
		late := time.Duration(95+i*4) * time.Second
		b.late(base[probes[1]][seq-1], late)
	}

	// 迟到 3 分钟：超过大部分流水线的容忍度，但仍在 15 分钟补报承诺之内。
	for i, seq := range []int{40, 41, 42} {
		b.late(base[probes[2]][seq-1], time.Duration(180+i*8)*time.Second)
	}

	// 迟到 20 分钟：超出控制面承诺的 15 分钟，任何水位线都兜不住，只能标记。
	b.late(base[probes[2]][34], 20*time.Minute)

	// 整批重放：同一批数据被重发一次，内容一字不差。
	{
		gb := base[probes[0]][41]
		b.add(item{batch: gb, at: gb.eventTime().Add(30 * time.Second)})
	}

	// 部分写入：先到的是只写进前 2 个点的一份，后面补上完整的 6 个点。
	// 原始位置要摘掉，否则整份先到，这个故事就没法演了。
	{
		gb := base[probes[0]][54]
		b.drop(gb)
		slot := gb.eventTime()
		first := slot.Add(500 * time.Millisecond)
		b.add(item{batch: gb, at: first, frozen: true, mutate: func(x *genBatch) {
			x.ProducedAt = first.Format(time.RFC3339)
			x.Points = x.Points[:2]
		}})
		second := slot.Add(2 * time.Second)
		b.add(item{batch: gb, at: second, frozen: true, mutate: func(x *genBatch) {
			x.ProducedAt = second.Format(time.RFC3339)
		}})
	}

	// 冲突：同一个批次号两份，有一项值不同。没有证据说谁对，整批拒。
	{
		gb := base[probes[1]][60]
		slot := gb.eventTime().Add(4 * time.Second)
		b.add(item{batch: gb, at: slot, mutate: func(x *genBatch) {
			x.ProducedAt = slot.Format(time.RFC3339)
			x.Points[0].Value = round3(x.Points[0].Value + 57)
		}})
	}

	// 周期重复上报：上一批还没被上层确认的点，在下一批里原样再报一次。
	// 信封的 batch_seq 变了，幂等键压不住；(probe_id, metric_id, acked_batch_seq) 才压得住。
	// 这一型来自 12-task：那边的每一条确认都会在相邻几批里重复出现，
	// 同一个批次里仍然一个 metric 只有一个点——多塞一个就会撞幂等键，那是另一条坑。
	{
		prev := base[probes[1]][49] // seq 50
		cur := base[probes[1]][50]  // seq 51
		ts := cur.eventTime()
		b.drop(cur)
		b.add(item{batch: cur, at: ts, mutate: func(x *genBatch) {
			x.Points[0] = prev.Points[0]
			x.Points[1] = prev.Points[1]
		}})
	}
	{
		prev := base[probes[0]][78] // seq 79
		cur := base[probes[0]][79]  // seq 80
		ts := cur.eventTime()
		b.drop(cur)
		b.add(item{batch: cur, at: ts, mutate: func(x *genBatch) {
			x.Points[0] = prev.Points[0]
		}})
	}

	// 批次号回绕：探针重启后批次号从 1 重新数。契约里没有 boot_id，
	// 前 3 批撞上已经用过的号（判冲突丢掉），后 3 批是新号（收下并标记回退）。
	{
		restart := baseSlot.Add(28*time.Minute + 30*time.Second)
		for i := 0; i < 6; i++ {
			ts := restart.Add(time.Duration(i) * slotGap)
			gb := &genBatch{
				ProbeID:    probes[0],
				BatchSeq:   uint64(i + 1),
				ProducedAt: ts.Format(time.RFC3339),
				Points:     makePoints(probes[0], ts, uint64(i+1), 0, 200+i, rng),
			}
			b.add(item{batch: gb, at: ts})
		}
	}

	// 下面是校验该拒的批次，一种一条。
	{
		ts := baseSlot.Add(time.Minute)
		gb := &genBatch{ProbeID: probes[0], BatchSeq: 0, ProducedAt: ts.Format(time.RFC3339)}
		gb.Points = makePoints(probes[0], ts, 1, 0, 900, rng)
		b.add(item{batch: gb, at: ts})
	}
	{
		// 点位里混进别的探针
		gb := base[probes[2]][89]
		ts := gb.eventTime().Add(40 * time.Second)
		b.add(item{batch: gb, at: ts, mutate: func(x *genBatch) {
			x.ProducedAt = ts.Format(time.RFC3339)
			x.Points[2].ProbeID = probes[0]
		}})
	}
	{
		// produced_at 不是 RFC3339（空格代替 T，没有时区）
		gb := base[probes[2]][94]
		ts := gb.eventTime().Add(50 * time.Second)
		b.add(item{batch: gb, at: ts, mutate: func(x *genBatch) {
			x.ProducedAt = ts.Format("2006-01-02 15:04:05")
		}})
	}
	{
		// 同批次 metric_id 重复：幂等键会撞，整批拒
		gb := base[probes[2]][99]
		ts := gb.eventTime().Add(60 * time.Second)
		b.add(item{batch: gb, at: ts, mutate: func(x *genBatch) {
			x.ProducedAt = ts.Format(time.RFC3339)
			dup := x.Points[0]
			dup.Value = round3(dup.Value + 11)
			x.Points = append(x.Points, dup)
		}})
	}
	{
		// 点位的 acked_ts 比 produced_at 晚 12 分钟：时钟没同步
		gb := base[probes[0]][107]
		ts := gb.eventTime().Add(70 * time.Second)
		b.add(item{batch: gb, at: ts, mutate: func(x *genBatch) {
			x.ProducedAt = ts.Format(time.RFC3339)
			x.Points[0].AckedTS = gb.eventTime().Add(12 * time.Minute).Format(time.RFC3339)
		}})
	}
	{
		// 空点位批次
		gb := base[probes[1]][29]
		ts := gb.eventTime().Add(80 * time.Second)
		b.add(item{batch: gb, at: ts, mutate: func(x *genBatch) {
			x.ProducedAt = ts.Format(time.RFC3339)
			x.Points = nil
		}})
	}

	// 截断的一行：上报进程在下发之前挂了，JSON 都没写完。
	{
		ts := baseSlot.Add(26 * time.Minute)
		b.add(item{
			raw: `{"probe_id":"probe-sh-a1-02","batch_seq":105,"produced_at":"` + ts.Format(time.RFC3339) + `","points":[{"probe_id":"probe-sh-a1-02","acked_batch_seq":11,"acked_ts":"` + ts.Format(time.RFC3339),
			at:  ts.Add(10 * time.Second),
		})
	}
	return b.items
}

// builder 负责增删，items 是切片、删元素会改长度，所以不能靠传值。
type builder struct{ items []item }

func (b *builder) add(it item) { b.items = append(b.items, it) }

// drop 摘掉原始位置的批次。摘掉之后由 mutate 过的副本顶上。
func (b *builder) drop(target *genBatch) {
	for i := range b.items {
		if b.items[i].batch == target && b.items[i].mutate == nil && !b.items[i].frozen {
			b.items = append(b.items[:i], b.items[i+1:]...)
			return
		}
	}
}

// late 把原始位置的批次摘掉，换成「事件时间不变、produced_at 推后」的版本。
// 不摘就成了同一批次到两次，那是重放，不是迟到。
func (b *builder) late(target *genBatch, delay time.Duration) {
	at := target.eventTime().Add(delay)
	b.drop(target)
	b.add(item{batch: target, at: at, mutate: func(x *genBatch) {
		x.ProducedAt = at.Format(time.RFC3339)
	}})
}

// eventTime 取一个批次的第一个点位的 acked_ts，即这一批覆盖的事件时间。
func (b *genBatch) eventTime() time.Time {
	if len(b.Points) == 0 {
		ts, _ := time.Parse(time.RFC3339, b.ProducedAt)
		return ts
	}
	ts, _ := time.Parse(time.RFC3339, b.Points[0].AckedTS)
	return ts
}

// shuffle 做局部乱序：同一条上报通路上的批次可能前后颠倒几档。
// 位移远小于水位线宽度时不影响结果，大于窗口宽度时就会开始丢——这正是要看的。
func shuffle(items []item) {
	for i := 0; i+3 < len(items); i++ {
		if i%9 != 4 || items[i].frozen || items[i+3].frozen {
			continue
		}
		items[i], items[i+3] = items[i+3], items[i]
	}
	for i := 0; i+20 < len(items); i++ {
		if i%53 != 7 || items[i].frozen || items[i+20].frozen {
			continue
		}
		items[i], items[i+20] = items[i+20], items[i]
	}
}

func makePoints(probe string, ts time.Time, ackedSeq uint64, pi, slot int, rng *lcg) []genPoint {
	p50 := round3(rttBase[pi] + float64(pi*7) + float64(rng.intn(40)) + wave(slot))
	p99 := round3(p50*3.4 + float64(rng.intn(120)))
	jitter := round3(8 + float64(rng.intn(120))/10)
	loss := 0.0
	if slot%17 == 0 {
		loss = 0.35
	}
	sent := 100.0
	lost := float64(rng.intn(3))
	vals := []float64{p50, p99, jitter, loss, sent, lost}

	pts := make([]genPoint, 0, len(metrics))
	for i, m := range metrics {
		pts = append(pts, genPoint{
			ProbeID:       probe,
			MetricID:      m,
			AckedBatchSeq: ackedSeq,
			AckedTS:       ts.Format(time.RFC3339),
			Value:         vals[i],
		})
	}
	return pts
}

// wave 给 RTT 加一个缓慢的起伏，聚合曲线不像白噪声。
func wave(slot int) float64 {
	return 15 * float64(slot%12) / 11
}

func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

// lcg 是定死参数的线性同余发生器，样本可复现，不依赖 math/rand 的版本行为。
type lcg struct{ s uint64 }

func (r *lcg) intn(n int) int {
	r.s = r.s*6364136223846793005 + 1442695040888963407
	return int((r.s >> 33) % uint64(n))
}
