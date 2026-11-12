# 07-counters · 计数器速率还原
GO ?= go

.PHONY: run test demo wraps warn clean

# 全量：三列速率对比、采集周期告警、真值对账、错包归因，产物落 out/
run:
	$(GO) run .

test:
	$(GO) vet ./... && $(GO) test ./...

# README 里的样张：只打每个端口的结论行与告警，不打逐区间明细
demo:
	$(GO) run . -quiet

# 回绕周期公式表。带上 -poll 就能判一组采集周期配得对不对
wraps:
	$(GO) run . -wraps -poll 10s

# 只看那个 10 秒采一次的 10G 口：告警长什么样
warn:
	$(GO) run . -port tor-a3f-02/Ethernet1/2 -quiet -out ""

clean:
	rm -rf out
