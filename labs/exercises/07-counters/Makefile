# 07-counters · 留白版：三种速率算法与四类归因要自己写
GO ?= go
REF := ../../07-counters

.PHONY: run check test answer clean

# 未实现时这里会打印「未实现」并返回 1，这是正常的
run:
	@$(GO) run . || echo "（退出码 1 是预期的：先实现 rate.go 的 ComputeRates）"

# 两份产物都对齐：rates.jsonl 与 attribution.json
check:
	@mkdir -p out
	@$(GO) run . >/dev/null 2>&1 || { echo "还没跑通：make run 看错误"; exit 1; }
	@ok=1; \
	for f in rates.jsonl attribution.json; do \
		if diff -u expected/$$f out/$$f; then \
			echo "通过：$$f 与 expected 一致"; \
		else \
			ok=0; \
		fi; \
	done; \
	if [ $$ok -eq 1 ]; then echo "两份产物全对。"; else \
		echo ""; echo "↑ 先让 rates 的分岔点站住，再抠 attribution 的 Reasons 措辞。"; exit 1; \
	fi

# 只做编译检查，不判分
test:
	$(GO) vet ./...

# 卡住时的退路：跑参考实现，输出落 out-answer/
answer:
	@cd $(REF) && $(GO) run . -out ../exercises/07-counters/out-answer
	@echo "参考输出在 out-answer/。对照着看，看完重写一遍，别复制。"

clean:
	rm -rf out out-answer
