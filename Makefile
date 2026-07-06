GOBIN := $(shell go env GOPATH)/bin
PROTOC_GEN_GO_PLUGIN := $(GOBIN)/protoc-gen-go-plugin

DIST_DIR := dist
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS_VER := -X main.version=$(VERSION)

.PHONY: tools proto proto-grpc proto-wasm build run clean

## tools: install the protobuf generators used by `make proto`
tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/knqyf263/go-plugin/cmd/protoc-gen-go-plugin@v0.9.0

## proto: regenerate gRPC and WASM Go code from proto/panel.proto
proto: proto-grpc proto-wasm

proto-grpc:
	mkdir -p pluginsdk/grpc
	PATH="$(GOBIN):$(PATH)" protoc -I. \
		--go_out=./pluginsdk/grpc --go_opt=paths=source_relative \
		--go-grpc_out=./pluginsdk/grpc --go-grpc_opt=paths=source_relative \
		./proto/panel.proto

proto-wasm:
	mkdir -p pluginsdk/wasm
	protoc --plugin=protoc-gen-go-plugin=$(PROTOC_GEN_GO_PLUGIN) -I. \
		--go-plugin_out=./pluginsdk/wasm --go-plugin_opt=paths=source_relative \
		./proto/panel.proto

## build: build the host server binary
build:
	mkdir -p $(DIST_DIR)
	go build -ldflags "$(LDFLAGS_VER)" -o $(DIST_DIR)/minimalpanel ./cmd

## run: run the host server
run:
	go run -ldflags "$(LDFLAGS_VER)" ./cmd

## clean: remove build artifacts
clean:
	rm -rf $(DIST_DIR) tmp
