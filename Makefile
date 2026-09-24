# Makefile for laxcode
# 将 Go 项目二进制编译到 bin/ 目录

BINARY   := laxcode
BUILD_DIR := bin
MAIN_PKG := ./cmd/main
WINDOWS_BUILD_DIR := $(BUILD_DIR)/win
WINDOWS_CC ?= x86_64-w64-mingw32-gcc
WINDOWS_SQLITE_PREFIX ?= $(shell brew --prefix sqlite 2>/dev/null)
WINDOWS_CGO_CFLAGS ?= $(if $(WINDOWS_SQLITE_PREFIX),-I$(WINDOWS_SQLITE_PREFIX)/include)

.PHONY: all build build-web build-windows vet test clean

all: build

# 编译主程序到 bin/laxcode
build:
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY) $(MAIN_PKG)
	@echo "built: $(BUILD_DIR)/$(BINARY)"

# Build the production browser bundle used by the standalone web executable.
build-web:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build

# Cross-compile the two prebuilt executables consumed by web.ps1. The backend
# uses SQLite CGO bindings and therefore requires a MinGW-w64 cross compiler.
build-windows: build-web
	@mkdir -p $(WINDOWS_BUILD_DIR)
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=$(WINDOWS_CC) CGO_CFLAGS="$(WINDOWS_CGO_CFLAGS)" go build -trimpath -ldflags="-s -w" -o $(WINDOWS_BUILD_DIR)/laxcode.exe $(MAIN_PKG)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags=webdist -trimpath -ldflags="-s -w" -o $(WINDOWS_BUILD_DIR)/laxcode-web.exe ./cmd/web
	@echo "built: $(WINDOWS_BUILD_DIR)/laxcode.exe"
	@echo "built: $(WINDOWS_BUILD_DIR)/laxcode-web.exe"

# 静态检查
vet:
	go vet ./...

# 单元测试
test:
	go test ./...

# 清理编译产物
clean:
	rm -f $(BUILD_DIR)/$(BINARY)
	@echo "cleaned: $(BUILD_DIR)/$(BINARY) (prebuilt $(WINDOWS_BUILD_DIR)/ artifacts preserved)"
