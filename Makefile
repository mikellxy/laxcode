# Makefile for laxcode
# 将 Go 项目二进制编译到 bin/ 目录

BINARY   := laxcode
BUILD_DIR := bin
MAIN_PKG := ./cmd/main

.PHONY: all build build-web build-cloud build-cloud-bash-image vet test clean

all: build

# Build once with network access; cloud Bash never pulls images at runtime.
build-cloud-bash-image:
	docker build -t laxcode-cloud-bash:local docker/cloud-bash

# 编译主程序到 bin/laxcode
build:
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY) $(MAIN_PKG)
	@echo "built: $(BUILD_DIR)/$(BINARY)"

# Build the production browser bundle used by the standalone web executable.
build-web:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build

# Produce the complete Linux amd64 NAS bundle, preserving nas/defaults/.
build-cloud: build-web
	sh ./build-cloud.sh

# 静态检查
vet:
	go vet ./...

# 单元测试
test:
	go test ./...

# 清理编译产物
clean:
	rm -f $(BUILD_DIR)/$(BINARY)
	@echo "cleaned: $(BUILD_DIR)/$(BINARY)"
