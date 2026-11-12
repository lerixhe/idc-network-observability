# 03-probe-matrix · 探测矩阵与健康评分
GO ?= go

.PHONY: run test demo cost clean

# 生成矩阵、按剧本跑一轮探测、打分并归因，三份产物落 out/
run:
	$(GO) run .

test:
	$(GO) vet ./... && $(GO) test ./...

# README 里的样张：再多打归因汇总、节点判定与真值对账
demo:
	$(GO) run . -demo

# 换探测间隔看成本怎么变：间隔翻倍，包速率减半，聚合存储几乎不动
cost:
	@for i in 1 5 10 60; do \
		echo "==== 探测间隔 $${i}s"; \
		$(GO) run . -interval $$i -out "" | grep -E "包速率|带宽|聚合存储|逐包原始"; \
	done

clean:
	rm -rf out
