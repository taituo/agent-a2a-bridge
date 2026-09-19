.PHONY: fmt vet test check build

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./...

check: fmt vet test
	go test -race -count=1 ./...

build:
	go build -o bin/a2actl ./cmd/a2actl
