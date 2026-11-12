# 05-int-sim · INT 逐跳数据还原
GO ?= go

.PHONY: run try test demo clean

# 只看 INT 元数据本身：不给真值，跟生产环境一样
run:
	$(GO) run . -truth ""

# 一条只有部分跳有数据的路径：盲区段吸收了多少
try:
	$(GO) run . -trace t-partial -truth ""

test:
	$(GO) vet ./... && $(GO) test ./...

# README 里引用的那份输出：带真值对账
demo:
	$(GO) run .

clean:
	rm -rf out
