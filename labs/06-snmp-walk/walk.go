package main

import (
	"errors"
	"fmt"
	"sort"
)

// WalkStats 是一次列遍历的代价。报表上的「PDU 数」「背退到几条」
// 都从这里来——采集工程的账必须能对上。
type WalkStats struct {
	Requests  int   // 发出几个请求（含重试）
	PDUs      int   // 有效 PDU 个数（不含重试）
	Varbinds  int   // 收到几条 varbind
	TooBig    int   // 被设备拒了几次
	Retries   int   // 超时重试次数
	Timeouts  int   // 超时次数
	QueueWait int64 // 排队等待毫秒
	Latency   int64 // 往返累计毫秒
	Reps      int   // 最终落到的 max-repetitions（背退之后）
	MaxPDUs   int   // 实际用掉的最大页数
}

// Merge 把一次列的统计累加进设备级的统计。
func (s *WalkStats) Merge(o WalkStats) {
	s.Requests += o.Requests
	s.PDUs += o.PDUs
	s.Varbinds += o.Varbinds
	s.TooBig += o.TooBig
	s.Retries += o.Retries
	s.Timeouts += o.Timeouts
	s.QueueWait += o.QueueWait
	s.Latency += o.Latency
	if o.MaxPDUs > s.MaxPDUs {
		s.MaxPDUs = o.MaxPDUs
	}
}

// Requester 发一次请求。限流、重试、记账都在采集器里，walk 只管从哪接着取。
type Requester func(req BulkRequest) (Response, error)

// MaxPDUPerColumn 是一列最多允许的页数，防死循环用的护栏。
// 正常取值 = 端口数/reps + 1；给到 512 已经远超任何单列。
const MaxPDUPerColumn = 512

// WalkColumn 按列遍历，模拟 GetBulk 的分批。
//
// 三件事同时在这里发生，缺一件都会采错：
//  1. 每页都从上一页最后一条的 OID 之后接着取（GetBulk 是 GetNext 的批量版）；
//  2. 收到不再以列 OID 开头的 varbind 就停——表尾之后永远还有别的 OID；
//  3. 设备回 tooBig 就把 max-repetitions 砍半重来，而不是放弃这一列。
func WalkColumn(col Column, reps int, request Requester, st *WalkStats) ([]Varbind, error) {
	if reps < 1 {
		reps = 1
	}
	st.Reps = reps
	var out []Varbind
	start := col.OID.Clone()
	for page := 0; ; page++ {
		if page >= MaxPDUPerColumn {
			return out, fmt.Errorf("列 %s 超过 %d 页仍未到表尾", col.Name, MaxPDUPerColumn)
		}
		resp, err := request(BulkRequest{Start: start, MaxRepetitions: reps})
		if err != nil {
			return out, err
		}
		switch resp.Error {
		case ErrTooBig:
			if reps <= 1 {
				return out, fmt.Errorf("列 %s 已经在 1 条/请求下仍然 tooBig", col.Name)
			}
			reps /= 2
			st.Reps = reps
			st.TooBig++
			continue
		case "":
		default:
			return out, errors.New(resp.Error)
		}
		st.PDUs++
		if page+1 > st.MaxPDUs {
			st.MaxPDUs = page + 1
		}
		progress := false
		done := false
		for _, vb := range resp.Varbinds {
			st.Varbinds++
			if vb.EndOfMibView {
				done = true
				break
			}
			if !vb.OID.HasPrefix(col.OID) {
				// 走出了这棵子树。停在这里，别把下一张表的数据混进来。
				done = true
				break
			}
			if start.Compare(vb.OID) >= 0 {
				// 没有往前走：设备重复返回同一个 OID，再要就是死循环。
				done = true
				break
			}
			out = append(out, vb)
			start = vb.OID
			progress = true
		}
		if done || !progress {
			return out, nil
		}
	}
}

// TableRow 是拼起来的一行：一个 ifIndex 上各列的值。
//
// 遍历的单位是列，用到的是行。这一步就是两者的转轴：同一个 ifIndex
// 的 12 个格子分散在 OID 树的不同枝上，只有采完全部列才拼得出一行。
type TableRow struct {
	Index  uint32
	Values map[string]Value
}

// Get 取一列，没有就返回 false——某个列遍历失败时这一格就是空的。
func (r *TableRow) Get(col string) (Value, bool) {
	v, ok := r.Values[col]
	return v, ok
}

// RowSet 是按 ifIndex 索引的行集合。
type RowSet map[uint32]*TableRow

// Add 把一列的全部 varbind 归到各自的行里。
func (rs RowSet) Add(col Column, vbs []Varbind) {
	for _, vb := range vbs {
		idx := vb.OID.Last()
		row, ok := rs[idx]
		if !ok {
			row = &TableRow{Index: idx, Values: map[string]Value{}}
			rs[idx] = row
		}
		row.Values[col.Name] = vb.Value
	}
}

// Sorted 按 ifIndex 升序返回，输出稳定。
func (rs RowSet) Sorted() []*TableRow {
	out := make([]*TableRow, 0, len(rs))
	for _, r := range rs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// MergeIn 把 ifXTable 的行并到 ifTable 的行上。两张表靠 ifIndex 对齐，
// 这也是它唯一的用处——当主键用就是本实验后半段的那个事故。
func (rs RowSet) MergeIn(other RowSet) {
	for idx, row := range other {
		dst, ok := rs[idx]
		if !ok {
			rs[idx] = row
			continue
		}
		for k, v := range row.Values {
			dst.Values[k] = v
		}
	}
}
