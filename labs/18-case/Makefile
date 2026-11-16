# 18-case · 端到端案例回放
GO ?= go

.PHONY: run test json demo clean

run:
	$(GO) run .

demo:
	@$(GO) run . | head -40

json:
	@$(GO) run . -json

test:
	$(GO) vet ./... && $(GO) test ./...

clean:
	rm -rf out
