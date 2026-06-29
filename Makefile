GOBIN := $(shell go env GOPATH)/bin
PROTOC_GEN_GO_PLUGIN := $(GOBIN)/protoc-gen-go-plugin

PLUGIN_DIR := plugins
DIST_DIR := dist

.PHONY: tools proto proto-grpc proto-wasm build run hello hello-wasm web-assets web-assets-wasm login login-wasm clean

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
	go build -o $(DIST_DIR)/minimalpanel ./cmd

## run: run the host server
run:
	go run ./cmd

## hello: build and package the hello WASM plugin into plugins/hello.plg
hello: hello-wasm
	rm -rf $(DIST_DIR)/hello_pkg
	mkdir -p $(DIST_DIR)/hello_pkg/Content $(PLUGIN_DIR)
	cp $(DIST_DIR)/hello.wasm $(DIST_DIR)/hello_pkg/Content/hello.wasm
	cp coreplugins/hello/info.yaml $(DIST_DIR)/hello_pkg/info.yaml
	cd $(DIST_DIR)/hello_pkg && zip -qr ../../$(PLUGIN_DIR)/hello.plg .
	rm -rf $(DIST_DIR)/hello_pkg

hello-wasm:
	mkdir -p $(DIST_DIR)
	GOOS=wasip1 GOARCH=wasm go build -o $(DIST_DIR)/hello.wasm -buildmode=c-shared ./coreplugins/hello

## web-assets: build and package web assets plugin into plugins/web-assets.plg
web-assets: web-assets-wasm
	rm -rf $(DIST_DIR)/web_assets_pkg
	mkdir -p $(DIST_DIR)/web_assets_pkg/Content/assets $(PLUGIN_DIR)
	cp $(DIST_DIR)/web_assets.wasm $(DIST_DIR)/web_assets_pkg/Content/web_assets.wasm
	cp coreplugins/webassets/info.yaml $(DIST_DIR)/web_assets_pkg/info.yaml
	cp -R coreplugins/webassets/assets/css $(DIST_DIR)/web_assets_pkg/Content/assets/css
	cp -R coreplugins/webassets/assets/icon $(DIST_DIR)/web_assets_pkg/Content/assets/icon
	cd $(DIST_DIR)/web_assets_pkg && zip -qr ../../$(PLUGIN_DIR)/web-assets.plg .
	rm -rf $(DIST_DIR)/web_assets_pkg

web-assets-wasm:
	mkdir -p $(DIST_DIR)
	GOOS=wasip1 GOARCH=wasm go build -o $(DIST_DIR)/web_assets.wasm -buildmode=c-shared ./coreplugins/webassets

## login: build and package login pages plugin into plugins/login.plg
login: login-wasm
	rm -rf $(DIST_DIR)/login_pkg
	mkdir -p $(DIST_DIR)/login_pkg/Content/pages $(PLUGIN_DIR)
	cp $(DIST_DIR)/login.wasm $(DIST_DIR)/login_pkg/Content/login.wasm
	cp coreplugins/login/info.yaml $(DIST_DIR)/login_pkg/info.yaml
	cp coreplugins/login/pages/login.html $(DIST_DIR)/login_pkg/Content/pages/login.html
	cp coreplugins/login/pages/logout.html $(DIST_DIR)/login_pkg/Content/pages/logout.html
	cd $(DIST_DIR)/login_pkg && zip -qr ../../$(PLUGIN_DIR)/login.plg .
	rm -rf $(DIST_DIR)/login_pkg

login-wasm:
	mkdir -p $(DIST_DIR)
	GOOS=wasip1 GOARCH=wasm go build -o $(DIST_DIR)/login.wasm -buildmode=c-shared ./coreplugins/login

## clean: remove build artifacts
clean:
	rm -rf $(DIST_DIR) $(PLUGIN_DIR)/hello.plg $(PLUGIN_DIR)/web-assets.plg $(PLUGIN_DIR)/login.plg tmp
