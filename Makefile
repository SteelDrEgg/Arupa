GOBIN := $(shell go env GOPATH)/bin
PROTOC_GEN_GO_PLUGIN := $(GOBIN)/protoc-gen-go-plugin

PLUGIN_DIR := plugins
DIST_DIR := dist

.PHONY: tools proto proto-grpc proto-wasm build run hello hello-wasm web-assets web-assets-wasm login login-wasm navigator navigator-wasm plugin-manager plugin-manager-wasm ssh ssh-grpc clean

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

## navigator: build and package navigator shell into plugins/navigator.plg
navigator: navigator-wasm
	rm -rf $(DIST_DIR)/navigator_pkg
	mkdir -p $(DIST_DIR)/navigator_pkg/Content/pages $(PLUGIN_DIR)
	cp $(DIST_DIR)/navigator.wasm $(DIST_DIR)/navigator_pkg/Content/navigator.wasm
	cp coreplugins/navigator/info.yaml $(DIST_DIR)/navigator_pkg/info.yaml
	cp coreplugins/navigator/pages/index.html $(DIST_DIR)/navigator_pkg/Content/pages/index.html
	cd $(DIST_DIR)/navigator_pkg && zip -qr ../../$(PLUGIN_DIR)/navigator.plg .
	rm -rf $(DIST_DIR)/navigator_pkg

navigator-wasm:
	mkdir -p $(DIST_DIR)
	GOOS=wasip1 GOARCH=wasm go build -o $(DIST_DIR)/navigator.wasm -buildmode=c-shared ./coreplugins/navigator

## plugin-manager: build and package plugin manager page into plugins/plugin-manager.plg
plugin-manager: plugin-manager-wasm
	rm -rf $(DIST_DIR)/plugin_manager_pkg
	mkdir -p $(DIST_DIR)/plugin_manager_pkg/Content/pages $(PLUGIN_DIR)
	cp $(DIST_DIR)/plugin_manager.wasm $(DIST_DIR)/plugin_manager_pkg/Content/plugin_manager.wasm
	cp coreplugins/pluginmanager/info.yaml $(DIST_DIR)/plugin_manager_pkg/info.yaml
	cp coreplugins/pluginmanager/pages/plugins.html $(DIST_DIR)/plugin_manager_pkg/Content/pages/plugins.html
	cd $(DIST_DIR)/plugin_manager_pkg && zip -qr ../../$(PLUGIN_DIR)/plugin-manager.plg .
	rm -rf $(DIST_DIR)/plugin_manager_pkg

plugin-manager-wasm:
	mkdir -p $(DIST_DIR)
	GOOS=wasip1 GOARCH=wasm go build -o $(DIST_DIR)/plugin_manager.wasm -buildmode=c-shared ./coreplugins/pluginmanager

## ssh: build and package SSH terminal gRPC plugin into plugins/ssh.plg
ssh: ssh-grpc
	rm -rf $(DIST_DIR)/ssh_pkg
	mkdir -p $(DIST_DIR)/ssh_pkg/Content/pages $(DIST_DIR)/ssh_pkg/Content/assets/terminal $(PLUGIN_DIR)
	cp $(DIST_DIR)/ssh-plugin $(DIST_DIR)/ssh_pkg/Content/ssh-plugin
	cp coreplugins/ssh/info.yaml $(DIST_DIR)/ssh_pkg/info.yaml
	cp coreplugins/ssh/pages/terminal.html $(DIST_DIR)/ssh_pkg/Content/pages/terminal.html
	cp -R coreplugins/ssh/assets/terminal/. $(DIST_DIR)/ssh_pkg/Content/assets/terminal
	cd $(DIST_DIR)/ssh_pkg && zip -qr ../../$(PLUGIN_DIR)/ssh.plg .
	rm -rf $(DIST_DIR)/ssh_pkg

ssh-grpc:
	mkdir -p $(DIST_DIR)
	go build -o $(DIST_DIR)/ssh-plugin ./coreplugins/ssh

## clean: remove build artifacts
clean:
	rm -rf $(DIST_DIR) $(PLUGIN_DIR)/hello.plg $(PLUGIN_DIR)/web-assets.plg $(PLUGIN_DIR)/login.plg $(PLUGIN_DIR)/navigator.plg $(PLUGIN_DIR)/plugin-manager.plg $(PLUGIN_DIR)/ssh.plg tmp
