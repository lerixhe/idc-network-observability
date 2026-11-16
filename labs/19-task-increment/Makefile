# 19-task-increment · 任务增量与坏点替换
GO ?= go

.PHONY: run test demo clean

run:
	$(GO) run .

demo:
	@$(GO) run . | head -30

test:
	$(GO) vet ./... && $(GO) test ./...

clean:
	rm -rf out
