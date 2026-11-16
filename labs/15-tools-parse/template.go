package main

import (
	"fmt"
	"strings"
)

// ColumnSpec 是模板里的一条列声明。
//
// 三件事在这里定死：样本里的列头叫什么（Headers 是别名表，同一列不同固件叫法不同）、
// 它对应哪个语义字段（Field）、它的单位是什么（Unit）。
// 单位写进模板，不从列名猜——列名叫 InOctets 而数值是包数的固件是存在的，
// 猜列名会在那一刻静默出错。
type ColumnSpec struct {
	Headers []string // 第一个是主名，其余是别名，匹配时忽略大小写与空白
	Field   Field
	Unit    Unit
}

// Template 是一份带版本号的解析模板。ID + Version 合起来是模板的身份证，
// 每条解析结果都记着是哪版模板解的——固件升级后排查解析事故，先看这个字段。
//
// FirmwarePrefix 决定「哪个版本用哪份模板」。设备输出里带固件版本（样本里是 @firmware），
// 前缀匹配比区间比较稳：厂商的版本号有 4.8(2)、5.170、3.9 三种写法，别去解析语义。
type Template struct {
	ID             string
	Version        int
	Vendor         string
	FirmwarePrefix []string
	Columns        []ColumnSpec
	Required       []Field // 缺一个就拒绝解析，不做「尽力而为」
}

// Ref 是「模板名@版本」，报告、JSON、日志里统一用它。
func (t Template) Ref() string {
	return fmt.Sprintf("%s@v%d", t.ID, t.Version)
}

// matchFirmware 用前缀匹配固件串。空前缀表示兜底模板。
func (t Template) matchFirmware(fw string) bool {
	if len(t.FirmwarePrefix) == 0 {
		return true
	}
	fw = strings.TrimSpace(fw)
	for _, p := range t.FirmwarePrefix {
		if strings.HasPrefix(fw, p) {
			return true
		}
	}
	return false
}

// spec 按字段找列声明。
func (t Template) spec(f Field) (ColumnSpec, bool) {
	for _, c := range t.Columns {
		if c.Field == f {
			return c, true
		}
	}
	return ColumnSpec{}, false
}

// Validate 是模板自检。解析器按下标取列，模板写错会静默取到错列，
// 所以模板一进程序就自己查一遍：字段不许重复、单位要和列头语义对得上。
//
// 「列头写着 Octets 但声明成 packets」这一条是真会发生的：
// 抄模板时从上一条记录改了一半，解析出来的字节数全成了包数。
func (t Template) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("模板缺 ID")
	}
	if t.Version <= 0 {
		return fmt.Errorf("模板 %s 没有版本号", t.ID)
	}
	if t.Vendor == "" {
		return fmt.Errorf("模板 %s 缺 vendor", t.ID)
	}

	seenField := map[Field]string{}
	seenHeader := map[string]string{}
	for _, c := range t.Columns {
		if c.Field == "" {
			return fmt.Errorf("模板 %s 有一列没声明 Field（列头 %v）", t.Ref(), c.Headers)
		}
		if len(c.Headers) == 0 {
			return fmt.Errorf("模板 %s 的字段 %s 没有列头", t.Ref(), c.Field)
		}
		if prev, ok := seenField[c.Field]; ok {
			return fmt.Errorf("模板 %s 里字段 %s 被两列抢了：%s 与 %s", t.Ref(), c.Field, prev, c.Headers[0])
		}
		seenField[c.Field] = c.Headers[0]

		for _, h := range c.Headers {
			key := normalizeHeader(h)
			if key == "" {
				return fmt.Errorf("模板 %s 有空列头", t.Ref())
			}
			if prev, ok := seenHeader[key]; ok {
				return fmt.Errorf("模板 %s 里列头 %q 出现两次（另一列是 %s）", t.Ref(), h, prev)
			}
			seenHeader[key] = c.Headers[0]

			if err := checkUnitAgainstHeader(c, h); err != nil {
				return fmt.Errorf("模板 %s: %w", t.Ref(), err)
			}
		}
	}

	for _, f := range t.Required {
		if _, ok := seenField[f]; !ok {
			return fmt.Errorf("模板 %s 要求字段 %s，但没声明这一列", t.Ref(), f)
		}
		if f == FieldPort {
			continue
		}
		if c, _ := t.spec(f); c.Unit == UnitUnset {
			return fmt.Errorf("模板 %s 的必需字段 %s 没有单位", t.Ref(), f)
		}
	}
	return nil
}

// checkUnitAgainstHeader 校验「列头语义」与「声明单位」不矛盾。
// 只查两个方向：列头写明 Octets/Bytes 的必须是 bytes，写明 Pkts/Packets 的必须是 packets。
// 反向不查——列头叫 Input 的列到底是包还是字节，只有设备知道，模板说了算。
func checkUnitAgainstHeader(c ColumnSpec, header string) error {
	l := strings.ToLower(header)
	looksBytes := strings.Contains(l, "octet") || strings.Contains(l, "byte")
	looksPackets := strings.Contains(l, "pkt") || strings.Contains(l, "packet")

	switch {
	case looksBytes && c.Unit != UnitBytes:
		return fmt.Errorf("列头 %q 是字节语义，单位却声明成 %s", header, unitText(c.Unit, true))
	case looksPackets && c.Unit != UnitPackets:
		return fmt.Errorf("列头 %q 是包语义，单位却声明成 %s", header, unitText(c.Unit, true))
	}
	return nil
}

// normalizeHeader 把列头压成可比较的形式：去空白、转小写。
// 只做这两件事——列头里的 / 与 - 都是信息（In/Out、Ucast/Mcast），不能删。
func normalizeHeader(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

// templates 是模板库。三份样本四个模板版本，其中 vendor-c 因为固件升级有两版。
//
// 加一个厂商 = 加一条 Template；改一份模板 = Version 加一，旧版本留着，
// 因为现网永远有一批设备没升级。
var templates = []Template{
	{
		ID:             "vendor-a-counters",
		Version:        1,
		Vendor:         "vendor-a",
		FirmwarePrefix: []string{"5."},
		Columns: []ColumnSpec{
			{Headers: []string{"Interface"}, Field: FieldPort},
			{Headers: []string{"InOctets"}, Field: FieldInBytes, Unit: UnitBytes},
			{Headers: []string{"InUcastPkts", "InPkts"}, Field: FieldInPackets, Unit: UnitPackets},
			{Headers: []string{"InErrors"}, Field: FieldInErrors, Unit: UnitCount},
			{Headers: []string{"OutOctets"}, Field: FieldOutBytes, Unit: UnitBytes},
			{Headers: []string{"OutUcastPkts", "OutPkts"}, Field: FieldOutPackets, Unit: UnitPackets},
			{Headers: []string{"OutErrors"}, Field: FieldOutErrors, Unit: UnitCount},
			{Headers: []string{"Speed"}, Field: FieldSpeed},
		},
		Required: []Field{FieldPort, FieldInBytes, FieldInPackets, FieldOutBytes, FieldOutPackets, FieldInErrors, FieldOutErrors},
	},
	{
		ID:             "vendor-b-counters",
		Version:        1,
		Vendor:         "vendor-b",
		FirmwarePrefix: []string{"4."},
		Columns: []ColumnSpec{
			{Headers: []string{"Port"}, Field: FieldPort},
			{Headers: []string{"InOctets"}, Field: FieldInBytes, Unit: UnitBytes},
			{Headers: []string{"InUcastPkts"}, Field: FieldInPackets, Unit: UnitPackets},
			{Headers: []string{"InErrors"}, Field: FieldInErrors, Unit: UnitCount},
			{Headers: []string{"OutOctets"}, Field: FieldOutBytes, Unit: UnitBytes},
			{Headers: []string{"OutUcastPkts"}, Field: FieldOutPackets, Unit: UnitPackets},
			{Headers: []string{"OutErrors"}, Field: FieldOutErrors, Unit: UnitCount},
			{Headers: []string{"InDiscards"}, Field: FieldInDiscards, Unit: UnitCount},
			{Headers: []string{"Speed"}, Field: FieldSpeed},
		},
		Required: []Field{FieldPort, FieldInBytes, FieldInPackets, FieldOutBytes, FieldOutPackets},
	},
	{
		// 3.9：出口给字节，没有包数。
		ID:             "vendor-c-counters",
		Version:        1,
		Vendor:         "vendor-c",
		FirmwarePrefix: []string{"3."},
		Columns: []ColumnSpec{
			{Headers: []string{"Interface"}, Field: FieldPort},
			{Headers: []string{"InOctets"}, Field: FieldInBytes, Unit: UnitBytes},
			{Headers: []string{"InPkts"}, Field: FieldInPackets, Unit: UnitPackets},
			{Headers: []string{"InErrors"}, Field: FieldInErrors, Unit: UnitCount},
			{Headers: []string{"OutOctets"}, Field: FieldOutBytes, Unit: UnitBytes},
			{Headers: []string{"OutErrors"}, Field: FieldOutErrors, Unit: UnitCount},
		},
		Required: []Field{FieldPort, FieldInBytes, FieldInPackets, FieldOutBytes, FieldOutErrors},
	},
	{
		// 4.2：出口的 OutOctets 被删掉，只剩 OutPkts；同时多了四个丢弃类列。
		// 出口单位从 bytes 变成 packets——旧的查询还在按 out_bytes 求和，
		// 结果从「有数」直接变成「空」，不会报错。
		ID:             "vendor-c-counters",
		Version:        2,
		Vendor:         "vendor-c",
		FirmwarePrefix: []string{"4."},
		Columns: []ColumnSpec{
			{Headers: []string{"Interface"}, Field: FieldPort},
			{Headers: []string{"InOctets"}, Field: FieldInBytes, Unit: UnitBytes},
			{Headers: []string{"InPkts"}, Field: FieldInPackets, Unit: UnitPackets},
			{Headers: []string{"InErrors"}, Field: FieldInErrors, Unit: UnitCount},
			{Headers: []string{"InDiscards"}, Field: FieldInDiscards, Unit: UnitCount},
			{Headers: []string{"InPause"}, Field: FieldInPause, Unit: UnitCount},
			{Headers: []string{"OutPkts"}, Field: FieldOutPackets, Unit: UnitPackets},
			{Headers: []string{"OutErrors"}, Field: FieldOutErrors, Unit: UnitCount},
			{Headers: []string{"OutDiscards"}, Field: FieldOutDiscards, Unit: UnitCount},
			{Headers: []string{"QueueDrops"}, Field: FieldQueueDrops, Unit: UnitCount},
			{Headers: []string{"Speed"}, Field: FieldSpeed},
		},
		Required: []Field{FieldPort, FieldInBytes, FieldInPackets, FieldOutPackets, FieldOutErrors},
	},
}

// selectTemplate 按厂商 + 固件版本挑模板。
//
// 两段：先按固件前缀匹配；匹配不到就退到该厂商版本号最大的模板，
// 并在 Warnings 里记一笔。这个回落是故意的——新固件上线时列通常只多不少，
// 老模板能解出大部分字段，比整份数据丢掉好。但回落必须留痕，否则
// 「解析成功率 100%」会掩盖「一半设备在用错的模板」。
func selectTemplate(vendor, firmware string) (Template, []string, error) {
	var cands []Template
	for _, t := range templates {
		if t.Vendor == vendor {
			cands = append(cands, t)
		}
	}
	if len(cands) == 0 {
		return Template{}, nil, fmt.Errorf("没有厂商 %q 的模板", vendor)
	}

	for _, t := range cands {
		if len(t.FirmwarePrefix) > 0 && t.matchFirmware(firmware) {
			return t, nil, nil
		}
	}

	best := cands[0]
	for _, t := range cands {
		if t.Version > best.Version {
			best = t
		}
	}
	warn := fmt.Sprintf("固件 %q 没有匹配的模板版本，回落到厂商最新的 %s；列头对不上就会报错，不会静默出数",
		firmware, best.Ref())
	return best, []string{warn}, nil
}

// validateTemplates 启动时全库自检。模板错在写的时候发现，不要等线上解析出鬼数据。
func validateTemplates() error {
	seen := map[string]bool{}
	for _, t := range templates {
		if err := t.Validate(); err != nil {
			return err
		}
		if seen[t.Ref()] {
			return fmt.Errorf("模板 %s 定义了两次", t.Ref())
		}
		seen[t.Ref()] = true
	}
	return nil
}
