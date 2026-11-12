# 12-task · 任务管控：拉取 vs 推送 + 四个失败模式
GO ?= go

.PHONY: run test demo scale modes flap tail clean

# 全量：拉取与推送对比、四个失败模式、对账，产物落 out/
run:
	$(GO) run .

test:
	$(GO) vet ./... && $(GO) test ./...

# 只比拉取与推送，不注入失败模式：延迟分布与请求数看得最清楚
demo:
	$(GO) run . -fail-modes none

# 十万探针只算量，不落批次（落批次有几百万行，不是本实验要量的事）
scale:
	$(GO) run . -n 100000 -out "" -quiet

# 换拉取周期，看延迟与请求数怎么换
modes:
	@$(GO) run . -poll 1s -modes pull -fail-modes none -out "" -quiet | grep -E "拉取|请求构成|轮询|变更到生效"
	@$(GO) run . -poll 5s -modes pull -fail-modes none -out "" -quiet | grep -E "拉取|请求构成|轮询"
	@$(GO) run . -poll 30s -modes pull -fail-modes none -out "" -quiet | grep -E "拉取|请求构成|轮询"

# 只看坏点替换震荡：抑制开与抑制关的对比
flap:
	$(GO) run . -modes pull -fail-modes flap -out "" -quiet

# 把最大补报延迟压到 1 分钟：变更延迟超过它就成缺口，看「补报上限」怎么吃掉数据
tail:
	$(GO) run . -max-report-delay 1m -fail-modes none -out "" -quiet

clean:
	rm -rf out
