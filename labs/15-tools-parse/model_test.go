package main

import (
	"math"
	"testing"
)

func nearly(a, b float64) bool {
	return math.Abs(a-b) < 1e-6*math.Max(1, math.Abs(b))
}

// SI 后缀必须展开到基本单位，Raw 保留设备原样。
// 设备上看到 1.29G，库里是 1290000000，对账全靠 Raw。
func TestParseCounter(t *testing.T) {
	cases := []struct {
		raw   string
		unit  Unit
		want  float64
		ok    bool
		isErr bool
	}{
		{raw: "1288412390", unit: UnitBytes, want: 1288412390, ok: true},
		{raw: "1.29G", unit: UnitBytes, want: 1.29e9, ok: true},
		{raw: "882.1M", unit: UnitBytes, want: 882.1e6, ok: true},
		{raw: "388.1K", unit: UnitPackets, want: 388.1e3, ok: true},
		{raw: "1,234,567", unit: UnitCount, want: 1234567, ok: true},
		{raw: "0", unit: UnitCount, want: 0, ok: true},
		{raw: "-", unit: UnitCount, ok: false},
		{raw: " n/a ", unit: UnitCount, ok: false},
		{raw: "abc", unit: UnitCount, isErr: true},
		{raw: "12%", unit: UnitCount, isErr: true},
	}

	for _, c := range cases {
		got, ok, err := parseCounter(c.raw, c.unit)
		if c.isErr {
			if err == nil {
				t.Errorf("%q 应该报错，却解析成 %v", c.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q 解析报错: %v", c.raw, err)
			continue
		}
		if ok != c.ok {
			t.Errorf("%q 的 ok = %v，想要 %v", c.raw, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if !nearly(got.Value, c.want) {
			t.Errorf("%q = %v，想要 %v", c.raw, got.Value, c.want)
		}
		if got.Raw == "" {
			t.Errorf("%q 的 Raw 丢了", c.raw)
		}
		if got.Unit != c.unit {
			t.Errorf("%q 的单位 = %q，想要 %q", c.raw, got.Unit, c.unit)
		}
	}
}

// 0 是设备报的真值，缺失是没有这一列。两者不能混。
func TestCounterMissingIsNotZero(t *testing.T) {
	p := PortCounters{Counters: map[Field]Counter{
		FieldInErrors: {Value: 0, Unit: UnitCount, Raw: "0"},
	}}

	if v, ok := p.Value(FieldInErrors); !ok || v != 0 {
		t.Errorf("设备报的 0 应该取得到，拿到 (%v, %v)", v, ok)
	}
	if v, ok := p.Value(FieldOutBytes); ok || v != 0 {
		t.Errorf("没声明的字段应该取不到，拿到 (%v, %v)", v, ok)
	}

	// 空结构不能 panic：模板缺列时下游照样会读。
	var empty PortCounters
	if _, ok := empty.Value(FieldInBytes); ok {
		t.Error("空结构不该取到值")
	}
}

// 平均帧长用同一行入向两个数相除，不是假设值。
func TestAvgFrameAndDerivedBytes(t *testing.T) {
	p := PortCounters{Counters: map[Field]Counter{
		FieldInBytes:    {Value: 1000, Unit: UnitBytes},
		FieldInPackets:  {Value: 10, Unit: UnitPackets},
		FieldOutPackets: {Value: 5, Unit: UnitPackets},
	}}

	frame, ok := p.AvgFrameBytes()
	if !ok || !nearly(frame, 100) {
		t.Fatalf("平均帧长 = %v（%v），想要 100", frame, ok)
	}
	derived, ok := p.DerivedOutBytes()
	if !ok || !nearly(derived, 500) {
		t.Fatalf("派生出口字节 = %v（%v），想要 500", derived, ok)
	}

	// 只有包数没有字节数时算不出帧长，必须返回 false 而不是 0。
	q := PortCounters{Counters: map[Field]Counter{
		FieldOutPackets: {Value: 5, Unit: UnitPackets},
	}}
	if _, ok := q.DerivedOutBytes(); ok {
		t.Error("没有入向字节数也算出派生值，说明在用假设的帧长")
	}
}

func TestFormatCount(t *testing.T) {
	cases := map[float64]string{
		0:          "0",
		1288412390: "1288412390",
		125.89:     "125.89",
		math.NaN(): "-",
	}
	for v, want := range cases {
		if got := formatCount(v); got != want {
			t.Errorf("formatCount(%v) = %q，想要 %q", v, got, want)
		}
	}
}

// 同一台设备两个固件版本之间，字段的出现与消失都要点出来。
// 漂移样本里：出口字节单位从 bytes 变成不可得（只剩包数）。
func TestUnitDrift(t *testing.T) {
	ds := loadSamples(t)
	changes := UnitDrift(ds.Captures)

	var outBytes *UnitChange
	for i := range changes {
		if changes[i].Device == "sw-c1f-01" && changes[i].Field == FieldOutBytes {
			outBytes = &changes[i]
			break
		}
	}
	if outBytes == nil {
		t.Fatalf("没抓到 out_bytes 的漂移，实际 %+v", changes)
	}
	if outBytes.From != "bytes（fw 3.9）" {
		t.Errorf("From = %q", outBytes.From)
	}
	if outBytes.To != "不可得（fw 4.2）" {
		t.Errorf("To = %q", outBytes.To)
	}

	// 出口包数反向出现：3.9 没有，4.2 有。这条是「列变多」的证据。
	var outPkts *UnitChange
	for i := range changes {
		if changes[i].Field == FieldOutPackets {
			outPkts = &changes[i]
			break
		}
	}
	if outPkts == nil || outPkts.To != "packets（fw 4.2）" || outPkts.From != "不可得（fw 3.9）" {
		t.Errorf("out_packets 漂移不对：%+v", outPkts)
	}
}

// 只有一次采集的设备不参与跨版本对比。
func TestUnitDriftSkipsSingleCapture(t *testing.T) {
	caps := []Capture{{Device: "sw-a", Firmware: "1.0", Ports: []PortCounters{
		{Counters: map[Field]Counter{FieldInBytes: {Value: 1, Unit: UnitBytes}}},
	}}}
	if got := UnitDrift(caps); len(got) != 0 {
		t.Errorf("单次采集应该有 0 处漂移，实际 %+v", got)
	}
}
