package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 样本文件是一套很小的文本格式：指令行 + 表格。指令行以 @ 开头，
// @table 之后第一行是列头，其后到空行 / 下一条指令之间的都是数据行。
//
// 为什么自己定格式而不是直接贴厂商原文：样本要能说明「哪部分是设备给的、
// 哪部分是采集侧补的」。@device / @firmware 是采集侧从登录会话里记下来的，
// 不是命令输出里的字段——真实设备只报「HOST: xxx」这种东西。
//
// 不认识的指令一律报错。手写样本时把 @firmware 打成 @firmwar，
// 静默忽略会让你以为模板选错了，其实元信息根本没读到。
type sampleMeta struct {
	kind     string
	device   string
	vendor   string
	firmware string
	command  string
	target   string
}

type sampleFile struct {
	Path     string
	Captures []Capture
	MTR      []MTRRun
}

// LoadDataset 读整个样本目录。目录里所有 .txt 都参与解析，按文件名排序，
// 保证多次运行的输出顺序一致——报告要能 diff。
func LoadDataset(dir string) (*Dataset, error) {
	if err := validateTemplates(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读样本目录 %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("%s 下没有 .txt 样本", dir)
	}

	ds := &Dataset{}
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("读 %s: %w", p, err)
		}
		sf, err := parseSampleFile(filepath.Base(p), string(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		ds.Captures = append(ds.Captures, sf.Captures...)
		ds.MTR = append(ds.MTR, sf.MTR...)
	}
	if len(ds.Captures) == 0 && len(ds.MTR) == 0 {
		return nil, fmt.Errorf("%s 下没有解出任何数据", dir)
	}
	return ds, nil
}

// parseSampleFile 把一份样本切成交替出现的「指令 + 表格」块。
func parseSampleFile(name, content string) (*sampleFile, error) {
	sf := &sampleFile{Path: name}
	meta := sampleMeta{kind: "device"}

	var (
		header     string
		rows       []string
		tableLine  int
		haveHeader bool
	)

	flush := func() error {
		if !haveHeader {
			return nil
		}
		caps, runs, err := parseTable(name, meta, header, rows, tableLine)
		if err != nil {
			return err
		}
		sf.Captures = append(sf.Captures, caps...)
		sf.MTR = append(sf.MTR, runs...)
		header, rows, haveHeader = "", nil, false
		return nil
	}

	for i, line := range strings.Split(content, "\n") {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			if err := flush(); err != nil {
				return nil, err
			}
		case strings.HasPrefix(trimmed, "#"):
			// 注释行不打断表格：设备的页脚也长这样（# 后面跟固件版本）。
		case isRuleLine(trimmed), isPagerLine(trimmed):
			// 分隔线与分页提示，一律跳过。
		case strings.HasPrefix(trimmed, "@"):
			if err := flush(); err != nil {
				return nil, err
			}
			key, val, err := splitDirective(trimmed)
			if err != nil {
				return nil, fmt.Errorf("第 %d 行: %w", lineNo, err)
			}
			switch key {
			case "table":
				haveHeader = false
				tableLine = lineNo + 1
			case "kind":
				if val != "device" && val != "mtr" {
					return nil, fmt.Errorf("第 %d 行: @kind 只能是 device 或 mtr，拿到 %q", lineNo, val)
				}
				meta.kind = val
			case "device":
				meta.device = val
			case "vendor":
				meta.vendor = val
			case "firmware":
				meta.firmware = val
			case "command":
				meta.command = val
			case "target":
				meta.target = val
			default:
				return nil, fmt.Errorf("第 %d 行: 不认识的指令 @%s", lineNo, key)
			}
		default:
			if !haveHeader {
				header = line
				haveHeader = true
				tableLine = lineNo
				rows = nil
				continue
			}
			rows = append(rows, line)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(sf.Captures) == 0 && len(sf.MTR) == 0 {
		return nil, fmt.Errorf("没解出任何数据：检查 @table 与列头行")
	}
	return sf, nil
}

// splitDirective 拆 "@key value"。值可以为空（@table）。
func splitDirective(line string) (key, val string, err error) {
	body := strings.TrimPrefix(line, "@")
	if body == "" {
		return "", "", fmt.Errorf("空的指令行")
	}
	if i := strings.IndexAny(body, " \t"); i >= 0 {
		return strings.ToLower(body[:i]), strings.TrimSpace(body[i+1:]), nil
	}
	return strings.ToLower(body), "", nil
}

// isRuleLine 认分隔线：整行只有 - = + _ 与空白。
// 设备输出的表格边框、分页符都长这样，当数据行解析必然报错。
func isRuleLine(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch r {
		case '-', '=', '+', '_', ' ', '\t':
		default:
			return false
		}
	}
	return true
}

// isPagerLine 认分页提示。华为是「---- More ----」，思科是「--More--」，
// 去掉空白后两者都含 --more，一个判断管两家。
func isPagerLine(s string) bool {
	l := strings.ToLower(strings.Join(strings.Fields(s), ""))
	return strings.Contains(l, "--more")
}

// parseTable 按 @kind 分流。device 段出 Capture，mtr 段出 MTRRun。
func parseTable(name string, meta sampleMeta, header string, rows []string, lineNo int) ([]Capture, []MTRRun, error) {
	switch meta.kind {
	case "mtr":
		run, err := parseMTRTable(name, meta, header, rows, lineNo)
		if err != nil {
			return nil, nil, err
		}
		return nil, []MTRRun{*run}, nil
	default:
		cap, err := parseDeviceTable(name, meta, header, rows, lineNo)
		if err != nil {
			return nil, nil, err
		}
		return []Capture{*cap}, nil, nil
	}
}

// parseDeviceTable 解析一段接口计数器表。
//
// 顺序固定：选模板 → 用模板的列头去绑样本的列头 → 查必需列 → 逐行切。
// 任何一步失败都返回错误，不做「尽力解析」。少一列还硬出数，
// 下游看到的是「这台设备的错误数一直是 0」，比没有数据糟得多。
func parseDeviceTable(name string, meta sampleMeta, header string, rows []string, lineNo int) (*Capture, error) {
	if meta.device == "" {
		return nil, fmt.Errorf("第 %d 行前缺 @device", lineNo)
	}
	if meta.vendor == "" {
		return nil, fmt.Errorf("%s: @device %s 缺 @vendor，选不了模板", name, meta.device)
	}

	tpl, warnings, err := selectTemplate(meta.vendor, meta.firmware)
	if err != nil {
		return nil, err
	}

	cols, err := buildLayout(header, tpl, alignLeft)
	if err != nil {
		return nil, fmt.Errorf("%s 第 %d 行: %w", name, lineNo, err)
	}

	cap := &Capture{
		Device:   meta.device,
		Vendor:   meta.vendor,
		Firmware: meta.firmware,
		Command:  meta.command,
		Template: tpl.Ref(),
		Source:   name,
		Warnings: warnings,
	}

	seenUnknown := map[string]bool{}
	for i, line := range rows {
		pc, unknown, err := parseDeviceRow(line, cols)
		if err != nil {
			return nil, fmt.Errorf("%s 第 %d 行（%s 的第 %d 条数据）: %w",
				name, lineNo+1+i, tpl.Ref(), i+1, err)
		}
		pc.Device = cap.Device
		pc.Vendor = cap.Vendor
		pc.Firmware = cap.Firmware
		pc.Command = cap.Command
		pc.Template = cap.Template
		for _, u := range unknown {
			if seenUnknown[u] {
				continue
			}
			seenUnknown[u] = true
			cap.Unknown = append(cap.Unknown, u)
		}
		cap.Ports = append(cap.Ports, pc)
	}
	if len(cap.Ports) == 0 {
		return nil, fmt.Errorf("%s: %s 的 %s 段没有数据行", name, meta.device, tpl.Ref())
	}
	return cap, nil
}

// parseDeviceRow 把一行切成一条 PortCounters。
func parseDeviceRow(line string, cols []column) (PortCounters, []string, error) {
	pc := PortCounters{Counters: map[Field]Counter{}}
	var unknown []string

	for _, c := range cols {
		raw := sliceColumn(line, c)
		if raw == "" {
			continue
		}
		switch c.field {
		case "":
			// 模板没声明的列：值留着，名字进丢弃清单，不静默吞掉。
			pc.Unknown = append(pc.Unknown, UnknownColumn{Name: c.name, Value: raw})
			unknown = append(unknown, c.name)
			continue
		case FieldPort:
			pc.Port = raw
			continue
		case FieldSpeed:
			pc.Speed = raw
			continue
		}

		cnt, ok, err := parseCounter(raw, c.unit)
		if err != nil {
			return pc, nil, fmt.Errorf("列 %s: %w", c.name, err)
		}
		if !ok {
			// 占位符：这一列这次没数，字段不写。写 0 会把缺失算成健康。
			continue
		}
		pc.Counters[c.field] = cnt
	}

	if pc.Port == "" {
		return pc, nil, fmt.Errorf("没取到端口名，检查 %v 的列位置", columnNames(cols))
	}
	return pc, unknown, nil
}

// alignMode 是表格的值对齐方式。设备计数器输出左对齐，mtr 的数值右对齐。
// 两种对齐的取值区间算法不同，混用会整体错一列——所以是显式参数，不靠猜。
type alignMode int

const (
	alignLeft alignMode = iota
	alignRight
)

// headerToken 是列头行里的一个列头，带它在行里的字节区间。
type headerToken struct {
	text  string
	start int
	end   int
}

// tokenizeHeader 按连续非空白字符切列头。
// 不按 strings.Fields 切——那样只知道顺序，不知道位置，值为空时会整体错位。
func tokenizeHeader(line string) []headerToken {
	var out []headerToken
	i := 0
	for i < len(line) {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			i++
		}
		out = append(out, headerToken{text: line[start:i], start: start, end: i})
	}
	return out
}

// column 是绑定后的列。field 为空表示模板里没有这一列（未知列）。
// 区间是 [start, end)，end 为 0 表示取到行尾。
type column struct {
	name  string
	field Field
	unit  Unit
	start int
	end   int
}

func columnNames(cols []column) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.name)
	}
	return out
}

// buildLayout 把样本列头与模板列头对齐，算出每列的取值区间。
//
// 这是「不写死列位置」的落地：区间全从列头行算，列顺序换了、列变多了都不影响，
// 只要列头文字还在。列头文字也变了（InOctets → InPkts）就得靠模板版本解决。
func buildLayout(header string, tpl Template, align alignMode) ([]column, error) {
	tokens := tokenizeHeader(header)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("列头行是空的")
	}

	used := make([]bool, len(tpl.Columns))
	cols := make([]column, 0, len(tokens))

	for i, tk := range tokens {
		key := normalizeHeader(tk.text)
		col := column{name: tk.text}
		for j, spec := range tpl.Columns {
			if used[j] {
				continue
			}
			if !headersMatch(spec.Headers, key) {
				continue
			}
			used[j] = true
			col.field, col.unit = spec.Field, spec.Unit
			break
		}

		switch align {
		case alignRight:
			// 右对齐：值贴着本列列头的右侧结束，起点取上一列列头的末尾。
			col.start = 0
			if i > 0 {
				col.start = tokens[i-1].end
			}
			col.end = tk.end
		default:
			col.start = tk.start
			if i == len(tokens)-1 {
				col.end = 0 // 末列取到行尾
			} else {
				col.end = tokens[i+1].start
			}
		}
		cols = append(cols, col)
	}

	var missing []string
	for _, f := range tpl.Required {
		found := false
		for _, c := range cols {
			if c.field == f {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, string(f))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("模板 %s 要求字段 %s，列头里没有对应的列（样本列头：%v）",
			tpl.Ref(), strings.Join(missing, "、"), columnNames(cols))
	}
	return cols, nil
}

// headersMatch 比较列头。忽略大小写与空白，其余字符一个不能少：
// InUcastPkts 与 InPkts 是两个不同的列头，靠模板的别名表打通。
func headersMatch(headers []string, key string) bool {
	for _, h := range headers {
		if normalizeHeader(h) == key {
			return true
		}
	}
	return false
}

// sliceColumn 从数据行里切出这一列的值。行短了就取到行尾，不补空格——
// 尾部列缺值是正常情况（老固件没有这一列，行尾就是短一截）。
func sliceColumn(line string, c column) string {
	if c.start >= len(line) {
		return ""
	}
	end := c.end
	if end <= c.start || end > len(line) {
		end = len(line)
	}
	return strings.TrimSpace(line[c.start:end])
}
