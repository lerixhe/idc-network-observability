package main

import (
	"fmt"
	"strings"
	"testing"
)

// loadSamples 是全部解析测试的入口：直接读 samples/ 目录，
// 测试里写死的期望值必须与样本文件对得上——样本改了，测试跟着红。
func loadSamples(t *testing.T) *Dataset {
	t.Helper()
	ds, err := LoadDataset("samples")
	if err != nil {
		t.Fatalf("加载样本失败: %v", err)
	}
	return ds
}

// captureOf 按设备 + 固件版本取一段采集。漂移样本一台设备两段，只能这样定位。
func captureOf(t *testing.T, ds *Dataset, device, firmware string) Capture {
	t.Helper()
	for _, c := range ds.Captures {
		if c.Device == device && c.Firmware == firmware {
			return c
		}
	}
	t.Fatalf("样本里没有 %s fw %s", device, firmware)
	return Capture{}
}

func fieldUnit(t *testing.T, p PortCounters, f Field) Unit {
	t.Helper()
	c, ok := p.Get(f)
	if !ok {
		t.Fatalf("%s %s 缺字段 %s", p.Device, p.Port, f)
	}
	return c.Unit
}

// 三份不同厂商的样本解出同一个结构：字段名、单位、语义都对齐。
// 这是「统一结构」的全部意义——下游查询不用知道设备是哪家的。
func TestThreeVendorsSameStructure(t *testing.T) {
	ds := loadSamples(t)

	cases := []struct {
		device   string
		firmware string
		template string
	}{
		{"sw-a3f-07", "5.170", "vendor-a-counters@v1"},
		{"sw-b1f-02", "4.8(2)", "vendor-b-counters@v1"},
		{"sw-c1f-01", "3.9", "vendor-c-counters@v1"},
		{"sw-c1f-01", "4.2", "vendor-c-counters@v2"},
	}

	for _, c := range cases {
		cap := captureOf(t, ds, c.device, c.firmware)
		if cap.Template != c.template {
			t.Errorf("%s fw %s 用了模板 %s，想要 %s", c.device, c.firmware, cap.Template, c.template)
		}
		if len(cap.Ports) != 3 {
			t.Errorf("%s fw %s 解析出 %d 条端口，想要 3 条", c.device, c.firmware, len(cap.Ports))
		}
		if len(cap.Unknown) == 0 && c.firmware != "3.9" {
			t.Errorf("%s fw %s 的丢弃清单是空的，样本里应该留了一列模板不认识的", c.device, c.firmware)
		}

		p := cap.Ports[0]
		if p.Port == "" {
			t.Errorf("%s fw %s 第一条没有端口名", c.device, c.firmware)
		}
		// 入向两族字段在三份样本里都齐，单位一致。
		if u := fieldUnit(t, p, FieldInBytes); u != UnitBytes {
			t.Errorf("%s 的 in_bytes 单位 = %s，想要 bytes", c.device, u)
		}
		if u := fieldUnit(t, p, FieldInPackets); u != UnitPackets {
			t.Errorf("%s 的 in_packets 单位 = %s，想要 packets", c.device, u)
		}
		if u := fieldUnit(t, p, FieldInErrors); u != UnitCount {
			t.Errorf("%s 的 in_errors 单位 = %s，想要 count", c.device, u)
		}
	}
}

// A 厂：值、单位、未知列、速度都要落到该落的地方。
func TestParseVendorA(t *testing.T) {
	cap := captureOf(t, loadSamples(t), "sw-a3f-07", "5.170")
	p := cap.Ports[0]

	if p.Port != "GE1/0/1" {
		t.Errorf("port = %s", p.Port)
	}
	if p.Speed != "10G" {
		t.Errorf("speed = %s，想要 10G", p.Speed)
	}
	want := map[Field]float64{
		FieldInBytes:    1288412390,
		FieldInPackets:  10233412,
		FieldOutBytes:   331204411,
		FieldOutPackets: 2881901,
		FieldInErrors:   0,
		FieldOutErrors:  0,
	}
	for f, v := range want {
		got, ok := p.Value(f)
		if !ok || got != v {
			t.Errorf("%s = %v（%v），想要 %v", f, got, ok, v)
		}
	}

	// CRC 模板没声明，值进丢弃清单而不是丢掉。
	if len(p.Unknown) != 1 || p.Unknown[0].Name != "CRC" || p.Unknown[0].Value != "0" {
		t.Errorf("丢弃清单 = %+v，想要 CRC=0", p.Unknown)
	}
	wantRaw := "1288412390"
	if c, _ := p.Get(FieldInBytes); c.Raw != wantRaw {
		t.Errorf("Raw = %q，想要 %q", c.Raw, wantRaw)
	}
}

// B 厂：值带 SI 后缀，解析要展开；单位不变。
func TestParseVendorBSISuffix(t *testing.T) {
	cap := captureOf(t, loadSamples(t), "sw-b1f-02", "4.8(2)")
	p := cap.Ports[0]

	cases := map[Field]float64{
		FieldInBytes:    1.29e9,
		FieldInPackets:  10.23e6,
		FieldOutBytes:   331.2e6,
		FieldOutPackets: 2.88e6,
		FieldInDiscards: 4,
	}
	for f, want := range cases {
		got, ok := p.Value(f)
		if !ok || !nearly(got, want) {
			t.Errorf("%s = %v（%v），想要 %v", f, got, ok, want)
		}
	}
	if c, _ := p.Get(FieldInBytes); c.Raw != "1.29G" {
		t.Errorf("Raw = %q，想要原始串 1.29G", c.Raw)
	}
	if len(cap.Unknown) != 1 || cap.Unknown[0] != "InMcastPkts" {
		t.Errorf("丢弃清单 = %v，想要 [InMcastPkts]", cap.Unknown)
	}
}

// 漂移样本：列变多、出口单位从 bytes 变成 packets。要求不崩，且缺失被如实标出。
func TestDriftCaptureNoPanic(t *testing.T) {
	ds := loadSamples(t)

	var cap Capture
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("解析漂移样本 panic 了: %v", r)
			}
		}()
		cap = captureOf(t, ds, "sw-c1f-01", "4.2")
	}()

	if cap.Template != "vendor-c-counters@v2" {
		t.Fatalf("模板 = %s，想要 vendor-c-counters@v2", cap.Template)
	}
	if len(cap.Ports) != 3 {
		t.Fatalf("漂移段解析出 %d 条端口，想要 3 条", len(cap.Ports))
	}

	p := cap.Ports[0]
	// 出口字节这一列在新固件里被删了：必须是「不可得」，不能是 0。
	if v, ok := p.Value(FieldOutBytes); ok {
		t.Errorf("4.2 段不该有 out_bytes，却拿到 %v", v)
	}
	if v, ok := p.Value(FieldOutPackets); !ok || v != 2904771 {
		t.Errorf("out_packets = %v（%v），想要 2904771", v, ok)
	}
	// 新增的四列都被模板认了，不算未知列。
	for _, f := range []Field{FieldInDiscards, FieldInPause, FieldOutDiscards, FieldQueueDrops} {
		if _, ok := p.Value(f); !ok {
			t.Errorf("4.2 段缺字段 %s，新增列没被模板认下来", f)
		}
	}
	// 真未知的那一列进丢弃清单。
	if len(p.Unknown) != 1 || p.Unknown[0].Name != "FecUncorrected" {
		t.Errorf("丢弃清单 = %+v，想要 FecUncorrected", p.Unknown)
	}
}

// 单位不一致要能换算，依据是同一行入向的字节数与包数之比。
func TestDriftAvgFrameDerived(t *testing.T) {
	cap := captureOf(t, loadSamples(t), "sw-c1f-01", "4.2")
	p := cap.Ports[0]

	frame, ok := p.AvgFrameBytes()
	if !ok {
		t.Fatal("算不出平均帧长")
	}
	want := 1299120412.0 / 10319311.0
	if !nearly(frame, want) {
		t.Errorf("平均帧长 = %.4f，想要 %.4f", frame, want)
	}
	forFrame, ok := p.DerivedOutBytes()
	if !ok {
		t.Fatal("算不出派生出口字节")
	}
	if !nearly(forFrame, want*2904771) {
		t.Errorf("派生出口字节 = %.1f，想要 %.1f", forFrame, want*2904771)
	}

	// 3.9 段有设备直接给的出口字节，不该走派生路径。
	old := captureOf(t, loadSamples(t), "sw-c1f-01", "3.9").Ports[0]
	if _, ok := old.Value(FieldOutBytes); !ok {
		t.Error("3.9 段应该有 out_bytes")
	}
	if v, ok := old.Value(FieldOutPackets); ok {
		t.Errorf("3.9 段不该有 out_packets，却拿到 %v", v)
	}
}

// 拿旧模板去解新固件的列头必须报错，不能解出一堆空字段。
func TestTemplateVersionMismatch(t *testing.T) {
	v1 := templates[2]
	if v1.Ref() != "vendor-c-counters@v1" {
		t.Fatalf("模板顺序变了：%s", v1.Ref())
	}
	_, err := buildLayout("Interface    InOctets    InPkts    InErrors    InDiscards    InPause    OutPkts    OutErrors    OutDiscards    QueueDrops    Speed", v1, alignLeft)
	if err == nil {
		t.Fatal("旧模板解新列头应该报错")
	}
	if !strings.Contains(err.Error(), "out_bytes") {
		t.Errorf("错误信息该点名缺 out_bytes，实际 %q", err.Error())
	}
}

// 列顺序换了还能对上。这条保证「不写死列位置」不是嘴上说说。
func TestLayoutColumnOrderIndependent(t *testing.T) {
	tpl, _, err := selectTemplate("vendor-a", "5.170")
	if err != nil {
		t.Fatal(err)
	}

	const w = 14
	header := fmt.Sprintf("%-*s%-*s%-*s%-*s%-*s%-*s%-*s%s", w, "Interface", w, "OutOctets", w, "InOctets",
		w, "InErrors", w, "OutUcastPkts", w, "InUcastPkts", w, "OutErrors", "Speed")
	row := fmt.Sprintf("%-*s%-*s%-*s%-*s%-*s%-*s%-*s%s", w, "GE1/0/1", w, "331204411", w, "1288412390",
		w, "0", w, "2881901", w, "10233412", w, "0", "10G")

	cols, err := buildLayout(header, tpl, alignLeft)
	if err != nil {
		t.Fatalf("列序换了就解不了：%v", err)
	}
	p, _, err := parseDeviceRow(row, cols)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if v, _ := p.Value(FieldInBytes); v != 1288412390 {
		t.Errorf("in_bytes = %v，列序一换就取错了", v)
	}
	if v, _ := p.Value(FieldOutBytes); v != 331204411 {
		t.Errorf("out_bytes = %v，列序一换就取错了", v)
	}
	if v, _ := p.Value(FieldInPackets); v != 10233412 {
		t.Errorf("in_packets = %v", v)
	}
	if v, _ := p.Value(FieldOutPackets); v != 2881901 {
		t.Errorf("out_packets = %v", v)
	}
}

// 行比列头短是正常的（末列缺值），不能报错；
// 但列头本身必须备齐模板的必需列，否则整段拒绝解析。
func TestShortRowTailMissing(t *testing.T) {
	tpl, _, err := selectTemplate("vendor-a", "5.170")
	if err != nil {
		t.Fatal(err)
	}
	header := fmt.Sprintf("%-14s%-14s%-14s%-14s%-14s%-14s%-14s%s",
		"Interface", "InOctets", "InUcastPkts", "InErrors", "OutOctets", "OutUcastPkts", "OutErrors", "Speed")
	cols, err := buildLayout(header, tpl, alignLeft)
	if err != nil {
		t.Fatal(err)
	}

	p, _, err := parseDeviceRow("GE1/0/1       1288412390", cols)
	if err != nil {
		t.Fatalf("短行不该报错: %v", err)
	}
	if v, ok := p.Value(FieldInBytes); !ok || v != 1288412390 {
		t.Errorf("in_bytes = %v（%v）", v, ok)
	}
	if _, ok := p.Value(FieldOutPackets); ok {
		t.Error("短行末尾的列不该有值")
	}
}

// 模板里没有的列头不能被静默忽略，也不能让整行解析失败：
// 值留在丢弃清单里，名字进 Capture.Unknown。
func TestUnknownColumnGoesToDropList(t *testing.T) {
	cap := captureOf(t, loadSamples(t), "sw-a3f-07", "5.170")
	if len(cap.Unknown) != 1 || cap.Unknown[0] != "CRC" {
		t.Fatalf("丢弃清单 = %v，想要 [CRC]", cap.Unknown)
	}
	for _, p := range cap.Ports {
		if len(p.Unknown) != 1 {
			t.Fatalf("%s 的未知列 = %+v", p.Port, p.Unknown)
		}
	}
}

// 指令打错字必须报错。静默忽略会让你以为模板选错了，其实元信息没读到。
func TestUnknownDirectiveRejected(t *testing.T) {
	body := "@kind device\n@device sw-x\n@firmwar 5.1\n@table\nInterface  InOctets\nGE1  1\n"
	_, err := parseSampleFile("bad.txt", body)
	if err == nil {
		t.Fatal("拼错的指令应该报错")
	}
	if !strings.Contains(err.Error(), "@firmwar") {
		t.Errorf("错误信息该点名拼错的指令，实际 %q", err.Error())
	}
}

// 缺厂商就选不了模板，必须直接报错。
func TestMissingVendorRejected(t *testing.T) {
	body := "@kind device\n@device sw-x\n@firmware 5.1\n@table\nInterface  InOctets\nGE1  1\n"
	_, err := parseSampleFile("novendor.txt", body)
	if err == nil || !strings.Contains(err.Error(), "@vendor") {
		t.Fatalf("缺 @vendor 应该报错并点名，实际 %v", err)
	}
}

// 分隔线与分页提示要跳过，不能当数据行。
func TestRuleLineAndPagerSkipped(t *testing.T) {
	body := `@kind device
@device sw-a3f-07
@vendor vendor-a
@firmware 5.170
@table
Interface    InOctets      InUcastPkts    InErrors    OutOctets    OutUcastPkts    OutErrors    Speed
--------------------------------------------------------------------------------
GE1/0/1      1288412390    10233412       0           331204411    2881901         0            10G
  ---- More ----
`
	sf, err := parseSampleFile("pager.txt", body)
	if err != nil {
		t.Fatalf("分隔行把解析搞崩了: %v", err)
	}
	if len(sf.Captures) != 1 || len(sf.Captures[0].Ports) != 1 {
		t.Fatalf("解出 %d 段 %d 条端口，想要 1 段 1 条", len(sf.Captures), len(sf.Captures[0].Ports))
	}
}

// 固件版本匹配不上时回落到厂商最新模板，但要留告警。
func TestTemplateFallbackWarns(t *testing.T) {
	tpl, warnings, err := selectTemplate("vendor-c", "9.0")
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Version != 2 {
		t.Errorf("回落版本 = %d，想要最新版 2", tpl.Version)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "回落") {
		t.Errorf("回落必须留告警，实际 %v", warnings)
	}
}

// 模板自检要能抓出「列头写着 Octets 却声明成 packets」这种手抄错误。
func TestTemplateValidateCatchesUnitConflict(t *testing.T) {
	bad := Template{
		ID: "x", Version: 1, Vendor: "v",
		Columns: []ColumnSpec{
			{Headers: []string{"Interface"}, Field: FieldPort},
			{Headers: []string{"InOctets"}, Field: FieldInBytes, Unit: UnitPackets},
		},
	}
	err := bad.Validate()
	if err == nil || !strings.Contains(err.Error(), "字节语义") {
		t.Fatalf("单位冲突应该被模板自检抓到，实际 %v", err)
	}

	noVersion := Template{ID: "y", Vendor: "v", Columns: bad.Columns}
	if err := noVersion.Validate(); err == nil {
		t.Fatal("没有版本号的模板应该被拒绝")
	}
}

// 库里的模板自己必须都是干净的。
func TestBuiltinTemplatesValid(t *testing.T) {
	if err := validateTemplates(); err != nil {
		t.Fatalf("内置模板自检没过: %v", err)
	}
}
