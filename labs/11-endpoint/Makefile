# 11-endpoint · IP → MAC → 交换机端口 → 机柜/U 位
GO ?= go
IP ?=

.PHONY: run query test demo clean

# 批量查 samples/queries.txt 里的 7 个 IP，结果打到终端并落 out/endpoint.jsonl
run:
	$(GO) run .

# 单查一个 IP：make query IP=10.32.7.18
query:
	@test -n "$(IP)" || { echo "用法：make query IP=10.32.7.18"; exit 1; }
	$(GO) run . -query $(IP)

test:
	$(GO) vet ./... && $(GO) test ./...

# README 里引用的那份输出：多打印结论分布、三个陷阱命中与三表规模
demo:
	@$(GO) run . -demo

clean:
	rm -rf out
