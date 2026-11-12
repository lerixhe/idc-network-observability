# 13-pipeline · 管道与存储
GO ?= go

.PHONY: run test demo window narrow capacity store feed samples clean

# 全量：编码对比、幂等重放、窗口乱序、存储、容量估算，产物落 out/
run:
	$(GO) run .

test:
	$(GO) vet ./... && $(GO) test ./...

# README 的样张：只打结论行，不打逐条明细
demo:
	$(GO) run . -quiet

# 窗口那一段：四档水位线并排，丢多少与晚多久对着看
window:
	$(GO) run . -only window

# 太窄的一档单独跑：1 分钟水位线，丢数据量与聚合偏差都放大
narrow:
	$(GO) run . -only window -watermark 1m

# 只算容量，不落盘。换参数就换一张表，比如 -pps 60000 -bytes 30 -compress 4
capacity:
	$(GO) run . -only capacity -out ""

# 存储那一段，跳过保留期演练，段留在 out/store 里看得见
store:
	$(GO) run . -only store -advance 0

# 拿 12-task 的落盘批次直接喂进来：契约对不对得上，一条命令就能验
feed:
	$(GO) run . -samples ../12-task/out/batches.jsonl -out "" -only dedup

# 重新生成样本（种子固定，结果可复现）
samples:
	$(GO) run samples/gen.go

clean:
	rm -rf out
