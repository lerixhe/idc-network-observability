package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	samplePath := flag.String("sample", "samples/case.json", "样本 JSON 路径")
	flag.Parse()

	s, err := LoadSampleFile(*samplePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取样本失败:", err)
		os.Exit(1)
	}

	Report(os.Stdout, s, Analyze(s))
}
