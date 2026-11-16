# 15-tools-parse · 设备命令输出解析（三厂商）+ mtr 路径解析
GO ?= go

.PHONY: run test demo clean

# 解析 samples/ 下三份计数器样本与一份 mtr，打主表与每跳丢包率，结果落 out/parsed.jsonl
run:
	$(GO) run .

test:
	$(GO) vet ./... && $(GO) test ./...

# README 里引用的那份输出：额外打印单位漂移、丢弃清单、派生换算
demo:
	@$(GO) run . -demo

clean:
	rm -rf out
