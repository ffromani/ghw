.PHONY: test
test: vet
	go test -v ./...

.PHONY: fmt
fmt:
	@echo "Running gofmt on all sources..."
	@gofmt -s -l -w .

.PHONY: fmtcheck
fmtcheck:
	@bash -c "diff -u <(echo -n) <(gofmt -d .)"

.PHONY: vet
vet:
	go vet ./...

.PHONY: clean
clean:
	@rm ghwc ghwc.wasm 2> /dev/null || :

ghwc:
	go build -o ghwc cmd/ghwc/main.go

ghwc.wasm:
	GOOS=wasip1 GOARCH=wasm go build -o ghwc.wasm cmd/ghwc/main.go
