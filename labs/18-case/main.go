package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	path := flag.String("case", "samples/case.json", "样本路径")
	jsonOut := flag.Bool("json", false, "以 JSON 输出（供脚本消费）")
	flag.Parse()

	c, err := LoadCase(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读样本失败：%v\n", err)
		os.Exit(1)
	}

	steps, conclusion := Replay(c)
	evidence := Evidence(c)

	if *jsonOut {
		out := map[string]any{
			"case_id":    c.CaseID,
			"steps":      steps,
			"evidence":   evidence,
			"conclusion": conclusion,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Printf("== 案例回放 %s ==\n", c.CaseID)
	fmt.Printf("窗口 %s → %s\n\n", c.Window.From, c.Window.To)
	for _, s := range steps {
		fmt.Printf("%2d. %s\n     看到：%s\n     判断：%s\n", s.No, s.Ask, s.Saw, s.Verdict)
	}
	fmt.Println("\n证据链：")
	for i, e := range evidence {
		fmt.Printf("  %d) %s\n", i+1, e)
	}
	fmt.Printf("\n结论：%s\n", conclusion)

	if err := os.MkdirAll("out", 0o755); err == nil {
		out := map[string]any{"case_id": c.CaseID, "steps": steps, "evidence": evidence, "conclusion": conclusion}
		if b, e := json.MarshalIndent(out, "", "  "); e == nil {
			_ = os.WriteFile("out/replay.json", b, 0o644)
		}
	}
	fmt.Println("\n产物：out/replay.json")
}
