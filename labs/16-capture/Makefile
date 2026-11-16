SAMPLE ?= samples/case.json

.PHONY: run test vet demo clean

## run: 用默认样本跑一遍归因
run:
	go run . -sample $(SAMPLE)

## test: 单元测试
test:
	go test ./...

## vet: 静态检查
vet:
	go vet ./...

## demo: 检查 + 测试 + 输出，一条命令看全貌
demo: vet test
	@echo "----- 归因输出 -----"
	@go run . -sample $(SAMPLE)

## clean: 清掉构建产物
clean:
	@rm -f 16-capture capture
	@go clean -testcache
