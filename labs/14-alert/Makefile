# 14-alert · 告警收敛与父子抑制
GO ?= go

.PHONY: run suppress degrade rules try test demo clean

# 四段全跑：父子抑制 / 血缘缺失 / 反例 / 三类规则，产物落 out/
run:
	$(GO) run .

# 只跑主剧本：一次 Spine 板卡重启的 87 条告警
suppress:
	$(GO) run . -mode suppress

# 只跑血缘缺失与反例两段
degrade:
	$(GO) run . -mode degrade

# 只跑阈值 / 持续时长 / 变化率三类规则对比
rules:
	$(GO) run . -mode rules

# 把「父必须更早」从 5 秒拉到 60 秒：看哪些紧跟着根因的症状被放行
try:
	$(GO) run . -mode suppress -suppress-lag 60s

test:
	$(GO) vet ./... && $(GO) test ./...

# README 引用的那份输出：先跑全量，再拿三套参数各跑一次做对照
demo:
	$(GO) run .
	@echo ""
	@echo "=== 对照一：退化窗口从 60s 收到 15s，时间窗关联大批失效 ==="
	$(GO) run . -mode degrade -degrade-window 15s -out ""
	@echo ""
	@echo "=== 对照二：恢复宽限从 120s 抬到 600s，补发全被吞掉 ==="
	$(GO) run . -mode suppress -resolve-grace 600s -out ""
	@echo ""
	@echo "=== 对照三：断点上限放到 400s，采集中断那条序列被算出斜率 ==="
	$(GO) run . -mode rules -max-gap 400s -rate 0.3 -out ""

clean:
	rm -rf out
