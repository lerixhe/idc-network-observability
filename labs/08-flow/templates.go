package main

import (
	"fmt"
	"time"
)

// 段状态。一个 data set = 一段。段是可信度判定的最小单位：
// 一条模板不对，这段里的每条记录都不可信，只丢一半没有意义。
const (
	reasonOK              = "ok"
	reasonTemplateMissing = "template_missing"
	reasonTemplateExpired = "template_expired"
	reasonLengthMismatch  = "length_mismatch"
	reasonUnknownField    = "unknown_field"
	reasonFieldType       = "field_type"
)

var reasonZH = map[string]string{
	reasonOK:              "可信",
	reasonTemplateMissing: "模板缺失",
	reasonTemplateExpired: "模板过期",
	reasonLengthMismatch:  "记录长度与模板不符",
	reasonUnknownField:    "模板含不认识的字段",
	reasonFieldType:       "字段取值类型不对",
}

// defaultTemplateTTL 是模板寿命的默认口径，取 RFC 7011 建议的 3 倍模板刷新周期，
// 工程上常用 30 分钟。这个数不是常量：exporter 那边改了刷新周期，这里就得跟着改，
// 所以做成命令行参数。
const defaultTemplateTTL = 30 * time.Minute

// templateKey 是模板的身份。必须带 exporter：模板 ID 是每个 exporter 自己分配的，
// 两个 exporter 都用 300 是很正常的事，只按 ID 缓存就会把 A 的模板套到 B 的记录上。
type templateKey struct {
	exporter string
	id       int
}

type cachedTemplate struct {
	spec   TemplateSpec
	seenMs int64
}

// templateCache 是采集器侧的模板表：按到达顺序喂模板，过期就忘掉。
type templateCache struct {
	ttlMs int64
	items map[templateKey]cachedTemplate
	// generations 记每个 key 见过几次模板定义。过期删除后这一项还要留着，
	// 否则「过期后重发」看起来就像第一次见到，重定义这件事就被吞了。
	generations map[templateKey]int
	// lastFields 也留着：过期之后重发一份字段变了的模板，还得能说出「变了」。
	lastFields map[templateKey][]string
}

func newTemplateCache(ttl time.Duration) *templateCache {
	return &templateCache{
		ttlMs:       ttl.Milliseconds(),
		items:       make(map[templateKey]cachedTemplate),
		generations: make(map[templateKey]int),
		lastFields:  make(map[templateKey][]string),
	}
}

// put 收下一条模板记录，返回这次定义的代数，以及「为什么算一次重定义」的说明。
//
// 重定义有三种来路：exporter 重启、模板刷新时改了字段、以及模板过期之后重发。
// 三种都按「以最新一条为准」处理，但都要报出来——字段变了的模板会让同一份
// 指标口径前后不一致，不报就是个哑巴账。字段没变的只是刷新，说明是空串。
func (c *templateCache) put(exporter string, spec TemplateSpec, atMs int64) (int, string) {
	key := templateKey{exporter: exporter, id: spec.ID}
	gen := c.generations[key] + 1
	c.generations[key] = gen

	_, hadOld := c.items[key]
	prev := c.lastFields[key]
	c.items[key] = cachedTemplate{spec: spec, seenMs: atMs}
	c.lastFields[key] = spec.Fields

	changed := !sameFields(prev, spec.Fields)
	switch {
	case gen == 1:
		return gen, ""
	case changed && !hadOld:
		return gen, "上一份已过期，且字段与之前不同"
	case changed:
		return gen, "字段与上一版不同"
	case !hadOld:
		return gen, "上一份已过期，这次是重发"
	default:
		return gen, ""
	}
}

// lookup 按段的时间点查模板。过期即删除，后面的段全部按「模板缺失」处理，
// 直到 exporter 重新发一份模板。
type lookupResult struct {
	spec  TemplateSpec
	state string // reasonOK / reasonTemplateMissing / reasonTemplateExpired
	age   time.Duration
}

func (c *templateCache) lookup(exporter string, id int, atMs int64) lookupResult {
	key := templateKey{exporter: exporter, id: id}
	item, ok := c.items[key]
	if !ok {
		return lookupResult{state: reasonTemplateMissing}
	}
	ageMs := atMs - item.seenMs
	if ageMs > c.ttlMs {
		delete(c.items, key)
		return lookupResult{
			state: reasonTemplateExpired,
			age:   time.Duration(ageMs) * time.Millisecond,
		}
	}
	return lookupResult{spec: item.spec, state: reasonOK, age: time.Duration(ageMs) * time.Millisecond}
}

func sameFields(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Segment 是一个 data set 的解析结论。不可信段不进 TopN，但要留一条账。
type Segment struct {
	Exporter   string
	Seq        int
	TS         string
	TemplateID int
	Trusted    bool
	Reason     string
	Records    int
	Dropped    int
	Note       string
}

// FlowRecord 是解码成功的一条流记录。Octets / Packets 已经是 exporter 聚合后的定量，
// 直接相加就是字节与包数，不需要再乘采样率——这是它和 sFlow 最大的区别。
type FlowRecord struct {
	Key FlowKey
	// Octets 是这条流的字节数，Packets 是它的报文数。
	Octets  int64
	Packets int64
}

// Event 是解码过程中的一条值得记下来的事，比如模板重定义。
type Event struct {
	TS       string
	Exporter string
	Kind     string
	Detail   string
}

// DecodeResult 是一次完整解码的产物。
type DecodeResult struct {
	Records         []FlowRecord
	Segments        []Segment
	Events          []Event
	DroppedSegments int
	TemplateDefs    int
}

// 事件类型。
const (
	eventTemplateRedefined = "template_redefined"
)

// DecodeExports 按到达顺序解码全部 export。
//
// 一律「不猜」：模板查不到就标不可信，长度对不上就整段丢弃。
// 猜一次的代价是字节数后面挂一串 0，报表看起来完全正常。
func DecodeExports(exports []NetFlowExport, ttl time.Duration) DecodeResult {
	cache := newTemplateCache(ttl)
	var res DecodeResult

	for _, e := range exports {
		for _, set := range e.Sets {
			if set.Template != nil {
				res.TemplateDefs++
				gen, why := cache.put(e.Exporter, *set.Template, e.tsMs)
				if why != "" {
					detail := fmt.Sprintf("模板 %d 第 %d 次定义，字段 [%s]（%s）",
						set.Template.ID, gen, joinFields(set.Template.Fields), why)
					res.Events = append(res.Events, Event{
						TS: e.TS, Exporter: e.Exporter, Kind: eventTemplateRedefined, Detail: detail,
					})
				}
				continue
			}

			seg, spec := decodeDataSet(cache, e, *set.Data)
			if !seg.Trusted {
				res.DroppedSegments++
			}
			res.Segments = append(res.Segments, seg)
			if !seg.Trusted {
				continue
			}
			res.Records = append(res.Records, decodeRecords(spec, *set.Data)...)
		}
	}
	return res
}

// decodeDataSet 判这一段可不可信，以及为什么不可信。它不产出记录，
// 记录由 decodeRecords 产出。判定与取值分开，两者各自测得清楚。
// 可信时第二个返回值是这一段的模板，交给 decodeRecords 用，避免再查一次缓存
// 万一期间模板过期、两次结论不一致。
func decodeDataSet(cache *templateCache, e NetFlowExport, ds DataSet) (Segment, TemplateSpec) {
	seg := Segment{
		Exporter:   e.Exporter,
		Seq:        e.Seq,
		TS:         e.TS,
		TemplateID: ds.TemplateID,
		Reason:     reasonOK,
	}

	look := cache.lookup(e.Exporter, ds.TemplateID, e.tsMs)
	switch look.state {
	case reasonTemplateMissing:
		seg.Trusted = false
		seg.Reason = reasonTemplateMissing
		seg.Dropped = len(ds.Records)
		seg.Note = fmt.Sprintf("模板 %d 没收到过（UDP 丢包、采集器重启、或模板包晚于数据包），字段布局未知，不猜",
			ds.TemplateID)
		return seg, TemplateSpec{}
	case reasonTemplateExpired:
		seg.Trusted = false
		seg.Reason = reasonTemplateExpired
		seg.Dropped = len(ds.Records)
		seg.Note = fmt.Sprintf("模板 %d 上次刷新在 %s 之前（寿命 %s），已作废",
			ds.TemplateID, look.age.Round(time.Second), cache.ttl())
		return seg, TemplateSpec{}
	}

	for _, f := range look.spec.Fields {
		if !knownFields[f] {
			seg.Trusted = false
			seg.Reason = reasonUnknownField
			seg.Dropped = len(ds.Records)
			seg.Note = fmt.Sprintf("模板 %d 里有本实验不认识的字段 %q，跳过整段", ds.TemplateID, f)
			return seg, TemplateSpec{}
		}
	}

	// 长度对不上就整段丢弃。半条记录拼出来的字节数比没有更坏：
	// 它看起来是个数，还会被当成真的进 TopN。
	for i, rec := range ds.Records {
		if len(rec) != len(look.spec.Fields) {
			seg.Trusted = false
			seg.Reason = reasonLengthMismatch
			seg.Dropped = len(ds.Records)
			seg.Note = fmt.Sprintf("模板 %d 有 %d 个字段，第 %d 条记录却有 %d 个值",
				ds.TemplateID, len(look.spec.Fields), i+1, len(rec))
			return seg, TemplateSpec{}
		}
		if _, err := recordFromValues(look.spec, rec); err != nil {
			seg.Trusted = false
			seg.Reason = reasonFieldType
			seg.Dropped = len(ds.Records)
			seg.Note = fmt.Sprintf("模板 %d 第 %d 条记录: %v", ds.TemplateID, i+1, err)
			return seg, TemplateSpec{}
		}
	}

	seg.Trusted = true
	seg.Records = len(ds.Records)
	return seg, look.spec
}

// decodeRecords 把可信段的值数组按模板字段顺序落成 FlowRecord。
// 调用前 decodeDataSet 已经判过长度与类型，这里只负责取值。
func decodeRecords(spec TemplateSpec, ds DataSet) []FlowRecord {
	out := make([]FlowRecord, 0, len(ds.Records))
	for _, values := range ds.Records {
		rec, err := recordFromValues(spec, values)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// recordFromValues 按模板字段顺序把值填进一条记录。
//
// 顺序就是口径。模板 [src, dst, packets, octets] 与 [src, dst, octets, packets]
// 长度一样、字段名一样，读错一次字节数就变成包数，数值还都长得像真的。
func recordFromValues(spec TemplateSpec, values []any) (FlowRecord, error) {
	if len(values) != len(spec.Fields) {
		return FlowRecord{}, fmt.Errorf("值有 %d 个，模板有 %d 个字段", len(values), len(spec.Fields))
	}
	var (
		rec                 FlowRecord
		srcIP, dstIP, proto string
		srcPort, dstPort    int
	)
	for i, field := range spec.Fields {
		v := values[i]
		switch field {
		case fieldSrcIP:
			s, err := asString(v)
			if err != nil {
				return FlowRecord{}, fmt.Errorf("%s: %w", field, err)
			}
			srcIP = s
		case fieldDstIP:
			s, err := asString(v)
			if err != nil {
				return FlowRecord{}, fmt.Errorf("%s: %w", field, err)
			}
			dstIP = s
		case fieldProto:
			s, err := asString(v)
			if err != nil {
				return FlowRecord{}, fmt.Errorf("%s: %w", field, err)
			}
			proto = s
		case fieldSrcPort:
			n, err := asInt(v)
			if err != nil {
				return FlowRecord{}, fmt.Errorf("%s: %w", field, err)
			}
			srcPort = int(n)
		case fieldDstPort:
			n, err := asInt(v)
			if err != nil {
				return FlowRecord{}, fmt.Errorf("%s: %w", field, err)
			}
			dstPort = int(n)
		case fieldOctets:
			n, err := asInt(v)
			if err != nil {
				return FlowRecord{}, fmt.Errorf("%s: %w", field, err)
			}
			if n < 0 {
				return FlowRecord{}, fmt.Errorf("%s 是负数 %d", field, n)
			}
			rec.Octets = n
		case fieldPackets:
			n, err := asInt(v)
			if err != nil {
				return FlowRecord{}, fmt.Errorf("%s: %w", field, err)
			}
			if n < 0 {
				return FlowRecord{}, fmt.Errorf("%s 是负数 %d", field, n)
			}
			rec.Packets = n
		default:
			return FlowRecord{}, fmt.Errorf("不认识的字段 %q", field)
		}
	}
	rec.Key = makeKey(srcIP, dstIP, proto, srcPort, dstPort)
	return rec, nil
}

func (c *templateCache) ttl() time.Duration { return time.Duration(c.ttlMs) * time.Millisecond }

func joinFields(fields []string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += ", "
		}
		out += f
	}
	return out
}

// trustedRecordCount 是可信段里的记录条数。
func trustedRecordCount(segs []Segment) int {
	n := 0
	for _, s := range segs {
		if s.Trusted {
			n += s.Records
		}
	}
	return n
}

// droppedRecordCount 是被丢弃的记录条数。
func droppedRecordCount(segs []Segment) int {
	n := 0
	for _, s := range segs {
		n += s.Dropped
	}
	return n
}
