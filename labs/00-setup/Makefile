# 00-setup · 第一个探针
GO ?= go

.PHONY: run try test demo clean

# 本地自测，不碰外网
run:
	$(GO) run .

# 打真实目标（改 samples/probe-matrix.json 或自己写一份）
try:
	$(GO) run . -targets samples/probe-matrix.json -count 10

test:
	$(GO) vet ./... && $(GO) test ./...

# 给 README 用的一份固定输出：时延字段打码，跨机器才能对照
# （sed 处理多字节单位不稳，这里交给 awk 按列替换）
demo:
	@$(GO) run . -count 5 | awk 'NR==1{print;next} /self/{print $$1,"     ",$$2,"     ",$$3,"     ",$$4,"     <rtt>  <rtt>";next}{print}'

clean:
	rm -rf out
