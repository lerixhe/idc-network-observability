# 06-snmp-walk · GetBulk 表遍历、档位限流与主键映射
GO ?= go

.PHONY: run try test demo udp clean

# 6 轮全量采集：五台设备三种命运，最后做换板重启后的重排对接
run:
	$(GO) run .

# 条数一律顶到 64：B / C 档设备回 tooBig，看采集器怎么砍半退回来
try:
	$(GO) run . -reps 64 -rebooted ""

test:
	$(GO) vet ./... && $(GO) test ./...

# 端口表换成老设备：两个口没有 alias，换板之后只剩 ifIndex 一条线索
demo:
	$(GO) run . -device tor-a3f-02

# 内置 agent 挂到 127.0.0.1 的真实 UDP 上，只采一台，几秒跑完
udp:
	$(GO) run . -wire udp -cycles 1 -only spine-01 -rebooted ""

clean:
	rm -rf out
