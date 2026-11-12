package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// 两种源在样本里的形状完全不同，这不是格式选择，是采集机制不同。
//
//   - sFlow：一行一个 datagram，里面若干个「报文头样本」。每个样本自带采样率，
//     字节数是样本报文长度 × 采样率。它回答「谁在打流量」。
//   - NetFlow / IPFIX：一行一个 export，里面若干个 set。模板 set 定义字段顺序，
//     数据 set 只带值数组，字节数由 exporter 自己聚合好了。它回答「谁和谁通了多少」。
//
// 样本是应用层语义的 JSON，不是真 UDP 字节流：真实报文里的字段 ID、变量长字段、
// padding、企业私有字段一律省掉，留下的是解析时必须面对的那几个决定——
// 采样率怎么放大、模板从哪来、模板过期了还认不认。

// sFlow 报文头样本里的一个字段集合。名字贴着 Wireshark 里 raw packet header 的读法。
type Header struct {
	SrcIP   string `json:"src_ip"`
	DstIP   string `json:"dst_ip"`
	Proto   string `json:"proto"`
	SrcPort int    `json:"src_port"`
	DstPort int    `json:"dst_port"`
	Bytes   int64  `json:"bytes"`
	TCPFlag string `json:"tcp_flags"`
}

// SFlowSample 是一条 flow sample：一个被抽中的报文头，加上该口的采样率。
//
// SamplingRate 是「平均每多少个报文抽一个」，1:1000 就填 1000。
// 填 0 表示设备没报采样率——这时样本字节不能乘任何数，只能标成不可信。
type SFlowSample struct {
	Seq          int    `json:"seq"`
	IfIndex      int    `json:"if_index"`
	SamplingRate int    `json:"sampling_rate"`
	Header       Header `json:"header"`
}

// SFlowDatagram 是一个 UDP 报文里的全部样本。一个 datagram 内的样本同属一个时刻。
type SFlowDatagram struct {
	TS      string        `json:"ts"`
	Agent   string        `json:"agent"`
	Seq     int           `json:"seq"`
	Samples []SFlowSample `json:"samples"`

	tsMs int64
}

// TemplateSpec 是一条模板记录：ID + 字段顺序。
//
// 字段顺序是模板的全部意义。同样 24 字节的记录，按 [octets, packets] 读和按
// [packets, octets] 读，字节数会变成包数。
type TemplateSpec struct {
	ID     int      `json:"id"`
	Fields []string `json:"fields"`
}

// DataSet 是一组数据记录。Records 里每条都是「按模板字段顺序排列的值」，
// 不是带 key 的对象——真实报文里没有 key，只有偏移量。
type DataSet struct {
	TemplateID int     `json:"template_id"`
	Records    [][]any `json:"records"`
}

// ExportSet 是一个 set，要么是模板，要么是数据。真实报文用 set ID 区分，这里用互斥字段。
type ExportSet struct {
	Template *TemplateSpec `json:"template,omitempty"`
	Data     *DataSet      `json:"data,omitempty"`
}

// NetFlowExport 是一个 UDP 报文里的全部 set。
type NetFlowExport struct {
	TS       string      `json:"ts"`
	Exporter string      `json:"exporter"`
	Seq      int         `json:"seq"`
	Sets     []ExportSet `json:"sets"`

	tsMs int64
}

// 本实验认识的字段。真实 IPFIX 里这些是数字 Element ID（8=sourceIPv4Address、
// 1=octetDeltaCount……），这里直接用名字，好手读。
const (
	fieldSrcIP   = "src_ip"
	fieldDstIP   = "dst_ip"
	fieldProto   = "proto"
	fieldSrcPort = "src_port"
	fieldDstPort = "dst_port"
	fieldOctets  = "octets"
	fieldPackets = "packets"
)

var knownFields = map[string]bool{
	fieldSrcIP:   true,
	fieldDstIP:   true,
	fieldProto:   true,
	fieldSrcPort: true,
	fieldDstPort: true,
	fieldOctets:  true,
	fieldPackets: true,
}

// FlowKey 是五元组。TopN 的 key 就是它。
//
// 用五元组而不是「源 IP」：同一个源 IP 可能同时在打十条流，只按源 IP 归并，
// 十条流会挤成一个条目，看起来像一条大象。
type FlowKey struct {
	SrcIP   string `json:"src_ip"`
	DstIP   string `json:"dst_ip"`
	Proto   string `json:"proto"`
	SrcPort int    `json:"src_port"`
	DstPort int    `json:"dst_port"`
}

// String 把五元组打成人读的一行，表格与产物都用它。
func (k FlowKey) String() string {
	src, dst := k.SrcIP, k.DstIP
	if k.SrcPort > 0 {
		src = fmt.Sprintf("%s:%d", k.SrcIP, k.SrcPort)
	}
	if k.DstPort > 0 {
		dst = fmt.Sprintf("%s:%d", k.DstIP, k.DstPort)
	}
	proto := k.Proto
	if proto == "" {
		proto = "any"
	}
	return src + " → " + dst + "/" + proto
}

// makeKey 把一条报文头的五元组取出。sFlow 与 NetFlow 走同一个 key 构造，
// 两个源的 TopN 才放得到一张表上对比。
func makeKey(src, dst, proto string, srcPort, dstPort int) FlowKey {
	return FlowKey{SrcIP: src, DstIP: dst, Proto: proto, SrcPort: srcPort, DstPort: dstPort}
}

// asInt 把 JSON 解出来的值取成整数。JSON 里数字是 float64，字符串形式也接受，
// 因为有些 exporter 的字段是字符串（比如 AS 号）。
func asInt(v any) (int64, error) {
	switch x := v.(type) {
	case float64:
		if x != math.Trunc(x) {
			return 0, fmt.Errorf("值 %v 不是整数", v)
		}
		return int64(x), nil
	case json.Number:
		n, err := strconv.ParseInt(x.String(), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("值 %q 不是整数", x.String())
		}
		return n, nil
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("值 %q 不是整数", x)
		}
		return n, nil
	case nil:
		return 0, fmt.Errorf("值是 null，不能当整数用")
	default:
		return 0, fmt.Errorf("值类型 %T 不能当整数用", v)
	}
}

// asString 取字符串字段。数字不接受——真报文里不存在「自动转字符串」这回事。
func asString(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("值类型 %T 不是字符串", v)
	}
	return s, nil
}

// parseTS 解 RFC3339 时间戳，顺手返回毫秒。样本手写，时间必须能读。
func parseTS(where, ts string) (int64, error) {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0, fmt.Errorf("%s: ts %q 不是 RFC3339: %w", where, ts, err)
	}
	return t.UnixMilli(), nil
}

// loadSFlow 读 sFlow 样本（JSONL，一行一个 datagram）并做结构校验。
func loadSFlow(path string) ([]SFlowDatagram, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []SFlowDatagram
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var d SFlowDatagram
		if err := json.Unmarshal([]byte(text), &d); err != nil {
			return nil, fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
		where := fmt.Sprintf("%s 第 %d 行", path, line)
		if d.Agent == "" {
			return nil, fmt.Errorf("%s: 缺少 agent", where)
		}
		if len(d.Samples) == 0 {
			return nil, fmt.Errorf("%s: datagram 里一个样本都没有", where)
		}
		tsMs, err := parseTS(where, d.TS)
		if err != nil {
			return nil, err
		}
		d.tsMs = tsMs
		for i, s := range d.Samples {
			if s.IfIndex <= 0 {
				return nil, fmt.Errorf("%s: 第 %d 个样本的 if_index 是 %d", where, i, s.IfIndex)
			}
			if s.SamplingRate < 0 {
				return nil, fmt.Errorf("%s: 第 %d 个样本的 sampling_rate 是 %d，不能为负", where, i, s.SamplingRate)
			}
			if s.Header.SrcIP == "" || s.Header.DstIP == "" {
				return nil, fmt.Errorf("%s: 第 %d 个样本的报文头缺 src_ip 或 dst_ip", where, i)
			}
			if s.Header.Bytes < 0 {
				return nil, fmt.Errorf("%s: 第 %d 个样本的 bytes 是 %d，不能为负", where, i, s.Header.Bytes)
			}
		}
		out = append(out, d)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: 一个 datagram 都没有", path)
	}
	return out, nil
}

// loadNetFlow 读 NetFlow/IPFIX 样本（JSONL，一行一个 export）并做结构校验。
//
// 这里只校验「能不能读」，不校验模板是否认识——模板的账由 templates.go 记，
// 因为「模板过期」是随时间变化的状态，不是文件本身的属性。
func loadNetFlow(path string) ([]NetFlowExport, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []NetFlowExport
	var lastMs int64 = math.MinInt64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var e NetFlowExport
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			return nil, fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
		where := fmt.Sprintf("%s 第 %d 行", path, line)
		if e.Exporter == "" {
			return nil, fmt.Errorf("%s: 缺少 exporter", where)
		}
		if len(e.Sets) == 0 {
			return nil, fmt.Errorf("%s: export 里一个 set 都没有", where)
		}
		tsMs, err := parseTS(where, e.TS)
		if err != nil {
			return nil, err
		}
		// 样本按到达顺序录。乱序会让模板年龄算成负数，静默算下去就会把
		// 一份过期模板当成新鲜的，所以在这里直接挡住。
		if tsMs < lastMs {
			return nil, fmt.Errorf("%s: ts %s 早于上一条 export，样本没有按到达顺序排列", where, e.TS)
		}
		lastMs = tsMs
		e.tsMs = tsMs
		for i, set := range e.Sets {
			switch {
			case set.Template != nil && set.Data != nil:
				return nil, fmt.Errorf("%s: 第 %d 个 set 同时是模板和数据", where, i)
			case set.Template != nil:
				if set.Template.ID <= 0 {
					return nil, fmt.Errorf("%s: 第 %d 个 set 的模板 ID 是 %d", where, i, set.Template.ID)
				}
				if len(set.Template.Fields) == 0 {
					return nil, fmt.Errorf("%s: 模板 %d 一个字段都没有", where, set.Template.ID)
				}
			case set.Data != nil:
				if set.Data.TemplateID <= 0 {
					return nil, fmt.Errorf("%s: 第 %d 个 set 的 template_id 是 %d", where, i, set.Data.TemplateID)
				}
				if len(set.Data.Records) == 0 {
					return nil, fmt.Errorf("%s: 模板 %d 的数据 set 里没有记录", where, set.Data.TemplateID)
				}
			default:
				return nil, fmt.Errorf("%s: 第 %d 个 set 既不是模板也不是数据", where, i)
			}
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: 一个 export 都没有", path)
	}
	return out, nil
}
