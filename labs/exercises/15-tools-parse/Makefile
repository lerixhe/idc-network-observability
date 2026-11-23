# 15-tools-parse · 留白版：列头驱动解析要自己写
GO ?= go
REF := ../../15-tools-parse

.PHONY: run check test answer clean

# 未实现时这里会打印「未实现」并返回 1，这是正常的
run:
	@$(GO) run . || echo "（退出码 1 是预期的：先实现 parse.go 的 buildLayout 与 parseDeviceRow）"

# 产物对齐：parsed.jsonl（12 条端口 + 1 段 mtr）
check:
	@mkdir -p out
	@$(GO) run . >/dev/null 2>&1 || { echo "还没跑通：make run 看错误"; exit 1; }
	@ok=1; \
	for f in parsed.jsonl; do \
		if diff -u expected/$$f out/$$f; then \
			echo "通过：$$f 与 expected 一致"; \
		else \
			ok=0; \
		fi; \
	done; \
	if [ $$ok -eq 1 ]; then echo "产物与 expected 一致。"; else \
		echo ""; echo "↑ 先让 12 行的端口与计数器对上，再抠模板版本与丢弃清单。"; exit 1; \
	fi

# 只做编译检查，不判分
test:
	$(GO) vet ./...

# 卡住时的退路：跑参考实现，输出落 out-answer/
answer:
	@cd $(REF) && $(GO) run . -out ../exercises/15-tools-parse/out-answer
	@echo "参考输出在 out-answer/。对照着看，看完重写一遍，别复制。"

clean:
	rm -rf out out-answer
