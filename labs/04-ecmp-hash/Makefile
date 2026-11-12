# 04-ecmp-hash · 哈希与路径覆盖
GO ?= go

.PHONY: run try test demo clean

# 按样本跑一遍：覆盖率表 + 预算表 + 故障可见性
run:
	$(GO) run .

# 换个哈希函数再跑：落桶全变，覆盖率曲线跟着变
try:
	$(GO) run . -hash crc32c-pow2 -demo

test:
	$(GO) vet ./... && $(GO) test ./...

# README 里引用的那份输出：多打 member 负载、期望探测次数与演示段
demo:
	$(GO) run . -demo

clean:
	rm -rf out
