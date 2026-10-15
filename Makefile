.PHONY: all clean tidy vendor test build install

all: clean install

clean:
	go clean

tidy:
	go mod tidy

vendor: tidy
	go mod vendor

test: vendor
	go test ./...

build: test
	go build

install: build
	go install
