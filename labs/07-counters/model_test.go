package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWrapPeriod(t *testing.T) {
	cases := []struct {
		name      string
		bits      int
		perSec    float64
		want      time.Duration
		wantOK    bool
		tolerance time.Duration
	}{
		{"1G 满速字节计数器", 32, 1e9 / 8, 34*time.Second + 357*time.Millisecond, true, 20 * time.Millisecond},
		{"10G 满速字节计数器", 32, 1e10 / 8, 3*time.Second + 435*time.Millisecond, true, 20 * time.Millisecond},
		{"1G 满速包计数器", 32, 1e9 / (84 * 8), 2886 * time.Second, true, 2 * time.Second},
		{"64 位字节计数器", 64, 1e9 / 8, 0, false, 0},
		{"速率为 0", 32, 0, 0, false, 0},
	}
	for _, c := range cases {
		got, ok := WrapPeriod(c.bits, c.perSec)
		if ok != c.wantOK {
			t.Fatalf("%s：ok = %v，期望 %v", c.name, ok, c.wantOK)
		}
		if !ok {
			continue
		}
		if d := got - c.want; d < -c.tolerance || d > c.tolerance {
			t.Errorf("%s：回绕周期 %s，期望 %s", c.name, got, c.want)
		}
	}
}

func TestSeriesValidate(t *testing.T) {
	ok := Series{
		Device: "sw", IfName: "Ethernet1/1", CounterBits: 32, SpeedBps: 1e9,
		Samples: []Sample{{AtMs: 0, Value: 1}, {AtMs: 1000, Value: 2}},
	}
	if err := ok.validate(); err != nil {
		t.Fatalf("合法样本被判非法：%v", err)
	}

	cases := []struct {
		name string
		edit func(*Series)
		want string
	}{
		{"缺 device", func(s *Series) { s.Device = "" }, "device"},
		{"缺 if_name", func(s *Series) { s.IfName = "" }, "if_name"},
		{"位宽不是 32/64", func(s *Series) { s.CounterBits = 16 }, "counter_bits"},
		{"速率为 0", func(s *Series) { s.SpeedBps = 0 }, "speed_bps"},
		{"样本不足两个", func(s *Series) { s.Samples = s.Samples[:1] }, "至少两个样本"},
		{"值超量程", func(s *Series) { s.Samples[0].Value = counter32Max + 1 }, "超出 32 位量程"},
		{"uptime 超量程", func(s *Series) { s.Samples[0].SysUpTimeTicks = uptimeMod }, "sysUpTime"},
		{"时间戳重复", func(s *Series) { s.Samples[1].AtMs = s.Samples[0].AtMs }, "两个样本"},
	}
	for _, c := range cases {
		s := ok
		s.Samples = append([]Sample(nil), ok.Samples...)
		c.edit(&s)
		err := s.validate()
		if err == nil {
			t.Errorf("%s：应当报错，实际通过", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：错误信息 %q 里没有 %q", c.name, err.Error(), c.want)
		}
	}
}

func TestOrderSamplesCountsMoved(t *testing.T) {
	in := []Sample{
		{AtMs: 0, Value: 0},
		{AtMs: 10000, Value: 1},
		{AtMs: 40000, Value: 4},
		{AtMs: 30000, Value: 3},
		{AtMs: 50000, Value: 5},
	}
	ordered, moved := orderSamples(in)
	if moved != 2 {
		t.Errorf("乱序位置数 = %d，期望 2", moved)
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i].AtMs <= ordered[i-1].AtMs {
			t.Fatalf("排序后时间戳没有严格递增：%dms 在 %dms 之后", ordered[i].AtMs, ordered[i-1].AtMs)
		}
	}
	// 文件里 40s 排在 30s 前面：30s 与 40s 这两个样本的时间位置与出现位置都对不上。
	wantMoved := []bool{false, false, true, true, false}
	for i, want := range wantMoved {
		if ordered[i].moved != want {
			t.Errorf("第 %d 个（%dms）moved = %v，期望 %v", i, ordered[i].AtMs, ordered[i].moved, want)
		}
	}
}

func TestNominalIntervalUsesMedian(t *testing.T) {
	samples := []Sample{
		{AtMs: 0}, {AtMs: 10000}, {AtMs: 20000}, {AtMs: 60000}, {AtMs: 70000},
	}
	if got := NominalInterval(samples); got != 10*time.Second {
		t.Fatalf("标称周期 = %s，期望 10s（均值会被那段 40s 拉歪，中位数不会）", got)
	}
}

func TestLoadSeriesFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "counters.json")
	content := `{"series":[{"device":"sw-a","if_name":"Ethernet1/1","if_alias":"to-x","if_index":1,
	  "counter":"ifInOctets","counter_bits":32,"speed_bps":1000000000,
	  "samples":[{"t_ms":0,"value":10,"sys_uptime_ticks":100},
	             {"t_ms":10000,"value":20,"sys_uptime_ticks":1100}]}]}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	series, err := loadSeries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("应当解析出 1 条序列，实际 %d", len(series))
	}
	if got := series[0].Key(); got != "sw-a/Ethernet1/1" {
		t.Errorf("端口主键 = %q", got)
	}
	if len(series[0].Samples) != 2 || series[0].Samples[1].Value != 20 {
		t.Errorf("样本解析不对：%+v", series[0].Samples)
	}

	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(empty, []byte(`{"series":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSeries(empty); err == nil {
		t.Error("空样本文件应当报错")
	}
}

func TestLoadShippedSamples(t *testing.T) {
	series, err := loadSeries("samples/counters.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 8 {
		t.Fatalf("样本里有 %d 条序列，期望 8", len(series))
	}
	truth, err := loadTruth("samples/truth.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range series {
		if _, ok := truth[s.Key()]; !ok {
			t.Errorf("%s 没有对应的真值", s.Key())
		}
		if err := s.validate(); err != nil {
			t.Errorf("%s 样本校验失败：%v", s.Key(), err)
		}
	}
}
