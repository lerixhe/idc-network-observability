# 11-endpoint · 留白版：三表联查要自己写
GO ?= go
REF := ../../11-endpoint

.PHONY: run check test answer clean

# 未实现时这里会打印「未实现」并返回 1，这是正常的
run:
	@$(GO) run . || echo "（退出码 1 是预期的：先实现 lookup.go 的 Locate）"

# 与 expected/endpoint.jsonl 逐行比对；通过时打印一行结论
check:
	@mkdir -p out
	@$(GO) run . >/dev/null 2>&1 || { echo "还没跑通：make run 看错误"; exit 1; }
	@if diff -u expected/endpoint.jsonl out/endpoint.jsonl; then \
		echo "通过：$(shell wc -l < expected/endpoint.jsonl | tr -d ' ') 行全对"; \
	else \
		echo ""; \
		echo "↑ 预期行（expected）与实际行（out）的差异。先让 cause/owner 全对，再抠 evidence 措辞。"; \
		exit 1; \
	fi

# 只做编译检查，不判分
test:
	$(GO) vet ./...

# 卡住时的退路：跑参考实现，把它的输出放进 out-answer/
# 故意不写进 out/，免得 check 比的是参考实现的输出
answer:
	@cd $(REF) && $(GO) run . -out ../exercises/11-endpoint/out-answer
	@echo "参考输出在 out-answer/endpoint.jsonl。对照着看，看完重写一遍，别复制。"

clean:
	rm -rf out out-answer
