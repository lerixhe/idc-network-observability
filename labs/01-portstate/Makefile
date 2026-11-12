# 01-portstate · 端口 down 的六成因判定
GO ?= go

.PHONY: run try test demo clean

# 按样本跑一遍判定，结果打到终端并落 out/verdicts.jsonl
run:
	$(GO) run .

# 换个抖动口径再跑：M 和 N 是策略，不是常量
try:
	$(GO) run . -flap-min 2

test:
	$(GO) vet ./... && $(GO) test ./...

# README 里引用的那份输出：多打印归属汇总、抖动清单、设备重启
demo:
	@$(GO) run . -summary

clean:
	rm -rf out
