# 02-device-alive · 一万台设备的存活监测
GO ?= go

.PHONY: run test demo four scale clean

# 四类故障全开，最常见的一次跑法
run:
	$(GO) run .

# 四类剧本各跑一遍，只看核对行。参数换了先跑这个，看漏报和误报怎么变
four:
	@for s in hard-down reboot cpu-stall probe-outage; do \
		echo "==== $$s"; \
		$(GO) run . -scenario $$s -quiet | grep -E "剧本：|事件总数|真实失联窗口|发现延迟|误报设备|劣化误判|探测侧故障|作废观测"; \
	done

test:
	$(GO) vet ./... && $(GO) test ./...

# 带事件明细的完整输出，README 里的样张就是它
demo:
	$(GO) run . -scenario all

# 一万台。判定层能跑多快，看这一行
scale:
	$(GO) run . -n 10000 -interval 10s -duration 1h -quiet

clean:
	rm -rf out
