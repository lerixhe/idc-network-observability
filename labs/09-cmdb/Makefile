# 09-cmdb · 脏台账对账
GO ?= go

.PHONY: run test demo apply guard clean

# 对账一遍：三类差异 + 纠正建议 + 建议写入的补丁。默认 dry-run，台账一个字节都不动
run:
	$(GO) run .

test:
	$(GO) vet ./... && $(GO) test ./...

# README 里的那段闭环：出差异单 -> 应用补丁到副本 -> 对副本再对一次账，差应为零
demo:
	@rm -rf out
	$(GO) run . -out out
	@echo ""
	@echo "===== 应用补丁到副本 out/cmdb.applied.json ====="
	@$(GO) run . -out "" -apply -confirm | grep -E "台账副本已写入|未被改写"
	@echo ""
	@echo "===== 对副本再对一次账 ====="
	$(GO) run . -ledger out/cmdb.applied.json -out ""

# 落库：要显式 -apply -confirm，而且只写到副本，写库留给人工流程
apply:
	$(GO) run . -apply -confirm

# 试一下那把锁：把台账本身当写库目标会被拒绝
guard:
	$(GO) run . -apply -confirm -write samples/cmdb.json

clean:
	rm -rf out
