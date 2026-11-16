# 20-probe-security · 下发签名与速率自限
GO ?= go

.PHONY: run test demo clean

run:
	$(GO) run .

demo:
	@$(GO) run . | sed -n '1,25p'

test:
	$(GO) vet ./... && $(GO) test ./...

clean:
	rm -rf out
