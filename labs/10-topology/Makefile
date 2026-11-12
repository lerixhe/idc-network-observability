# 10-topology · 拓扑多源融合与事件去重
GO ?= go

.PHONY: run topology events try test demo clean

# 两段都跑：先融合四份拓扑快照，再把四源事件压成事件，产物落 out/
run:
	$(GO) run .

# 只跑拓扑融合：LLDP / STP / 路由邻居 / MAC 反推
topology:
	$(GO) run . -mode topology

# 只跑事件去重：Trap / syslog / 计数器归零 / 探测失败
events:
	$(GO) run . -mode events

# 把合并窗口从 5 分钟压到 30 秒：同一个端口隔 30 秒以上的重复不再合并
try:
	$(GO) run . -mode events -dedup-window 30s

test:
	$(GO) vet ./... && $(GO) test ./...

# README 里引用的那份输出：先跑全量，再用两套参数各跑一次做对照
demo:
	$(GO) run .
	@echo ""
	@echo "=== 对照一：端口数下限抬到 64，48 口全 down 不再折叠成设备级事件 ==="
	$(GO) run . -mode events -burst-min-ports 64 -out ""
	@echo ""
	@echo "=== 对照二：合并窗口压到 30s，超窗的同指纹事件重新裂开 ==="
	$(GO) run . -mode events -dedup-window 30s -out ""

clean:
	rm -rf out
