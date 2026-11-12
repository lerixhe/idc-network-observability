# 08-flow · 流数据解析与采样误差
GO ?= go

.PHONY: run flows sampling try merge test demo clean

# 两个源都解析，再跑一遍采样误差模拟，产物落 out/
run:
	$(GO) run .

# 只解析样本：段账 + TopN
flows:
	$(GO) run . -mode flows

# 只跑采样误差：同一份真值，三个采样率
sampling:
	$(GO) run . -mode sampling

# 把模板寿命挪短：过期线一动，昨天还可信的段今天就成垃圾
try:
	$(GO) run . -mode flows -template-ttl 3m

# 演示双计错误：两个源直接相加
merge:
	$(GO) run . -mode flows -merge

test:
	$(GO) vet ./... && $(GO) test ./...

# README 里引用的那份输出
demo:
	$(GO) run .

clean:
	rm -rf out
