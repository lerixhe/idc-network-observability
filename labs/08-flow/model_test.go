package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlowKeyString(t *testing.T) {
	cases := []struct {
		key  FlowKey
		want string
	}{
		{makeKey("10.0.0.1", "10.0.0.2", "tcp", 443, 51000), "10.0.0.1:443 → 10.0.0.2:51000/tcp"},
		{makeKey("10.0.0.1", "10.0.0.2", "udp", 0, 0), "10.0.0.1 → 10.0.0.2/udp"},
		{makeKey("10.0.0.1", "10.0.0.2", "", 443, 0), "10.0.0.1:443 → 10.0.0.2/any"},
	}
	for _, c := range cases {
		if got := c.key.String(); got != c.want {
			t.Errorf("String() = %q, 期望 %q", got, c.want)
		}
	}
}

func TestAsInt(t *testing.T) {
	if n, err := asInt(float64(8421)); err != nil || n != 8421 {
		t.Fatalf("asInt(8421.0) = %d, %v", n, err)
	}
	if n, err := asInt("8421"); err != nil || n != 8421 {
		t.Fatalf("asInt(\"8421\") = %d, %v", n, err)
	}
	for _, bad := range []any{8421.5, nil, true} {
		if _, err := asInt(bad); err == nil {
			t.Errorf("asInt(%v) 应当报错", bad)
		}
	}
}

func TestAsStringRejectsNumber(t *testing.T) {
	if _, err := asString(float64(1)); err == nil {
		t.Fatal("asString 收到数字应当报错，不能自动转成 \"1\"")
	}
	if s, err := asString("tcp"); err != nil || s != "tcp" {
		t.Fatalf("asString(\"tcp\") = %q, %v", s, err)
	}
}

func TestLoadSFlowSampleFile(t *testing.T) {
	dgs, err := loadSFlow(filepath.Join("samples", "sflow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dgs) != 6 {
		t.Fatalf("样本里应当有 6 个 datagram，实际 %d", len(dgs))
	}
	total, zeroRate := 0, 0
	for _, d := range dgs {
		if d.Agent != "10.0.0.11" {
			t.Errorf("agent = %q", d.Agent)
		}
		if d.tsMs == 0 {
			t.Errorf("datagram %d 没有解出时间戳", d.Seq)
		}
		for _, s := range d.Samples {
			total++
			if s.SamplingRate == 0 {
				zeroRate++
			}
		}
	}
	if total != 18 || zeroRate != 1 {
		t.Fatalf("样本 18 个、采样率为 0 的 1 个，实际 %d 个、%d 个", total, zeroRate)
	}
}

func TestLoadSFlowRejectsBadLines(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"缺 agent", `{"ts":"2026-05-12T10:00:00Z","samples":[{"if_index":1,"sampling_rate":100,"header":{"src_ip":"a","dst_ip":"b"}}]}`, "agent"},
		{"没有样本", `{"ts":"2026-05-12T10:00:00Z","agent":"a","samples":[]}`, "一个样本都没有"},
		{"时间戳不是 RFC3339", `{"ts":"2026-05-12 10:00:00","agent":"a","samples":[{"if_index":1,"sampling_rate":100,"header":{"src_ip":"a","dst_ip":"b"}}]}`, "RFC3339"},
		{"缺五元组端点", `{"ts":"2026-05-12T10:00:00Z","agent":"a","samples":[{"if_index":1,"sampling_rate":100,"header":{"src_ip":"a"}}]}`, "src_ip"},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "sflow.jsonl")
		if err := os.WriteFile(path, []byte(c.body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := loadSFlow(path)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: 错误应当是「%s」，实际 %v", c.name, c.want, err)
		}
	}
}

func TestLoadNetFlowSampleFile(t *testing.T) {
	exports, err := loadNetFlow(filepath.Join("samples", "netflow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 9 {
		t.Fatalf("样本里应当有 9 个 export，实际 %d", len(exports))
	}
	if exports[0].tsMs != parseTSMs(t, "2026-05-12T10:00:00Z") {
		t.Errorf("第一个 export 的时间戳解错了: %d", exports[0].tsMs)
	}
}

func TestLoadNetFlowRejectsOutOfOrder(t *testing.T) {
	body := `{"ts":"2026-05-12T10:05:00Z","exporter":"a","seq":2,"sets":[{"data":{"template_id":1,"records":[["x"]]}}]}
{"ts":"2026-05-12T10:00:00Z","exporter":"a","seq":1,"sets":[{"data":{"template_id":1,"records":[["x"]]}}]}
`
	path := filepath.Join(t.TempDir(), "netflow.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadNetFlow(path)
	if err == nil || !strings.Contains(err.Error(), "到达顺序") {
		t.Fatalf("倒序的 export 应当被挡住，实际 %v", err)
	}
}

func TestLoadNetFlowRejectsMalformedSets(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"空 set", `{"ts":"2026-05-12T10:00:00Z","exporter":"a","sets":[]}`, "一个 set 都没有"},
		{"模板没有字段", `{"ts":"2026-05-12T10:00:00Z","exporter":"a","sets":[{"template":{"id":1,"fields":[]}}]}`, "一个字段都没有"},
		{"数据 set 没有记录", `{"ts":"2026-05-12T10:00:00Z","exporter":"a","sets":[{"data":{"template_id":1,"records":[]}}]}`, "没有记录"},
		{"两种都不像", `{"ts":"2026-05-12T10:00:00Z","exporter":"a","sets":[{}]}`, "既不是模板也不是数据"},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "netflow.jsonl")
		if err := os.WriteFile(path, []byte(c.body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := loadNetFlow(path)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: 错误应当是「%s」，实际 %v", c.name, c.want, err)
		}
	}
}

func parseTSMs(t *testing.T, ts string) int64 {
	t.Helper()
	ms, err := parseTS("test", ts)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}
