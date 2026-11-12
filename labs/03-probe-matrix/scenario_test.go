package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeTemp 把一段 JSON 写进临时目录，用来测样本校验。
func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写临时样本失败：%v", err)
	}
	return path
}

func TestLoadScenarioRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "缺 window_start",
			body:    `{"window_s":60,"interval_s":1,"incidents":[{"kind":"host_down","nodes":["srv-1"]}]}`,
			wantErr: "window_start",
		},
		{
			name:    "窗口短于探测间隔",
			body:    `{"window_start":"t0","window_s":1,"interval_s":60,"incidents":[{"kind":"host_down","nodes":["srv-1"]}]}`,
			wantErr: "短于探测间隔",
		},
		{
			name:    "没有注入事件",
			body:    `{"window_start":"t0","window_s":60,"interval_s":1,"incidents":[]}`,
			wantErr: "没有注入任何事件",
		},
		{
			name:    "事件类型不认识",
			body:    `{"window_start":"t0","window_s":60,"interval_s":1,"incidents":[{"kind":"meteor","nodes":["srv-1"]}]}`,
			wantErr: "不认识",
		},
		{
			name:    "链路丢包没写 links",
			body:    `{"window_start":"t0","window_s":60,"interval_s":1,"incidents":[{"kind":"link_loss","loss":0.1}]}`,
			wantErr: "没写 links",
		},
		{
			name:    "链路丢包率越界",
			body:    `{"window_start":"t0","window_s":60,"interval_s":1,"incidents":[{"kind":"link_loss","links":[["a","b"]],"loss":1.5}]}`,
			wantErr: "loss",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadScenario(writeTemp(t, "incidents.json", c.body))
			if err == nil {
				t.Fatal("期望报错，实际通过了")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("错误信息 %q 里没有 %q", err, c.wantErr)
			}
		})
	}
}

func TestSimulateOnMiniTopology(t *testing.T) {
	tp := miniTopology(t)
	m, err := GenerateMatrix(tp, 1)
	if err != nil {
		t.Fatalf("生成矩阵失败：%v", err)
	}
	sc := Scenario{
		WindowStart: "t0",
		WindowS:     60,
		IntervalS:   1,
		Incidents: []Incident{
			{Kind: eventAgentDown, Nodes: []string{"srv-1a"}},
			{Kind: eventHostDown, Nodes: []string{"srv-1b"}},
			{Kind: eventNICRxDown, Nodes: []string{"srv-2a"}},
			{Kind: eventLinkLoss, Links: [][]string{{"tor-1", "spine-1"}}, Loss: 0.5, LatencyUs: 100},
		},
	}
	results, truth, err := Simulate(tp, m, sc)
	if err != nil {
		t.Fatalf("仿真失败：%v", err)
	}
	if !truth.HostDown["srv-1b"] || !truth.AgentDown["srv-1a"] || !truth.NICRxDown["srv-2a"] {
		t.Fatalf("真值表不对：%+v", truth)
	}

	byKey := indexResults(results)
	for _, p := range m.Pairs {
		k := pairKey(p.Src, p.Dst)
		r, has := byKey[k]
		switch {
		case p.Src == "srv-1a", p.Src == "srv-1b":
			// 掉线探针的行必须是空的：没有记录，不是「记录了 0 个包」。
			if has {
				t.Fatalf("%s 是掉线探针，不该有记录", k)
			}
		case p.Dst == "srv-1b", p.Dst == "srv-2a":
			if !has || r.Recv != 0 {
				t.Fatalf("%s 的目标整机不可达，应该全丢，得到 %+v", k, r)
			}
		default:
			if !has {
				t.Fatalf("%s 应该有记录", k)
			}
			if r.Sent != 60 {
				t.Fatalf("%s 的 sent = %d，期望 60", k, r.Sent)
			}
		}
	}

	// 确定性：同样的输入跑两次，结果必须逐字节一样。
	again, _, err := Simulate(tp, m, sc)
	if err != nil {
		t.Fatalf("第二次仿真失败：%v", err)
	}
	if !reflect.DeepEqual(results, again) {
		t.Fatal("两次仿真的结果不同，样本没法复现")
	}
}

func TestSimulateRejectsUnknownLink(t *testing.T) {
	tp := miniTopology(t)
	m, _ := GenerateMatrix(tp, 1)
	sc := Scenario{
		WindowStart: "t0", WindowS: 60, IntervalS: 1,
		Incidents: []Incident{{Kind: eventLinkLoss, Links: [][]string{{"tor-1", "border-01"}}, Loss: 0.1}},
	}
	if _, _, err := Simulate(tp, m, sc); err == nil {
		t.Fatal("注入一条不存在的链路应该报错")
	}
}

// TestSamplePipeline 是样本上的全流程答案核对。改了样本或阈值，这个测试会先挂。
func TestSamplePipeline(t *testing.T) {
	tp, err := LoadTopology("samples/topology.json")
	if err != nil {
		t.Fatalf("加载拓扑失败：%v", err)
	}
	sc, err := LoadScenario("samples/incidents.json")
	if err != nil {
		t.Fatalf("加载剧本失败：%v", err)
	}
	m, err := GenerateMatrix(tp, sc.IntervalS)
	if err != nil {
		t.Fatalf("生成矩阵失败：%v", err)
	}
	results, truth, err := Simulate(tp, m, sc)
	if err != nil {
		t.Fatalf("仿真失败：%v", err)
	}
	a := Classify(m, indexResults(results), sc.WindowS/sc.IntervalS)
	rec := Evaluate(m, a, truth)

	if len(rec.HostDownHit) != 3 {
		t.Fatalf("主机不可达命中 %v，期望样本里那 3 台", rec.HostDownHit)
	}
	if len(rec.ProbeHit) != 1 || len(rec.TargetHit) != 1 {
		t.Fatalf("探针侧命中 %v、对端侧命中 %v，样本里各只有 1 台", rec.ProbeHit, rec.TargetHit)
	}
	if n := len(rec.HostDownFalse) + len(rec.ProbeFalse) + len(rec.TargetFalse); n != 0 {
		t.Fatalf("节点级判定出现 %d 个误判：%v %v %v", n, rec.HostDownFalse, rec.ProbeFalse, rec.TargetFalse)
	}
	if rec.LinkFalse != 0 {
		t.Fatalf("网络侧出现 %d 条假阳性", rec.LinkFalse)
	}

	// 任务要求的对照：探针自己关掉的那几个节点，一条 network_side 都不许有。
	for _, dead := range []string{"server-b03-01", "server-b03-02", "server-b03-03", "server-a01-02"} {
		for _, s := range a.Scores {
			if s.Src == dead && s.Verdict == VerdictNetwork {
				t.Fatalf("%s 是探针侧问题，却被判成网络侧异常（%s）", dead, pairKey(s.Src, s.Dst))
			}
		}
	}

	// 真故障：跨坏链路的对必须绝大多数判成 network_side，且共同链路指对。
	const badLink = "spine-a1↔tor-a03"
	net, missed, overridden := 0, 0, 0
	for _, p := range m.Pairs {
		hit := false
		for _, k := range p.Path {
			if k == badLink {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		// 跨了坏链路但已经被「主机不可达 / 探针侧 / 对端侧」先一步定性的对，
		// 不算漏判。分母只算「本来该由网络侧结论承接」的那一批。
		switch verdictOf(a, p.Src, p.Dst) {
		case VerdictNetwork:
			net++
		case VerdictOK:
			missed++
		default:
			overridden++
		}
	}
	if net+missed+overridden == 0 {
		t.Fatal("没有一条对的路径跨过注入的坏链路，样本和拓扑对不上")
	}
	if net == 0 {
		t.Fatal("跨坏链路的所有对都没判成 network_side")
	}
	if ratio := float64(net) / float64(net+missed); ratio < 0.95 {
		t.Fatalf("该由网络侧承接的对只有 %.0f%% 被判出来（%d 判出 / %d 漏判 / %d 被更早结论盖掉）",
			ratio*100, net, missed, overridden)
	}
	if a.SharedLink != badLink {
		t.Fatalf("共同链路段 = %q，期望 %q", a.SharedLink, badLink)
	}
}
