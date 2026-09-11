# my_agent build & dev tasks
.PHONY: build run test test-race vet tidy

build:
	go build ./...

run:
	go run ./cmd/my_agent -config configs/config.yaml

test:
	go test ./...

test-race:
	go test ./... -race

vet:
	go vet ./...

tidy:
	go mod tidy
