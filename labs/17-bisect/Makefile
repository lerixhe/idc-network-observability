GO ?= go

.PHONY: run test vet demo clean

run:
	$(GO) run . -file samples/case.json

test:
	$(GO) vet ./... && $(GO) test ./...

vet:
	$(GO) vet ./...

demo: run

clean:
	$(GO) clean ./...
	rm -f bisect
