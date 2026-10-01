# Gopherite Makefile
GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  = -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build test vet fmt fmtcheck bench clean

## build: 构建单二进制到 dist/
build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/gopherite ./cmd/gopherite

## test: 运行全部测试
test:
	$(GO) test -race ./...

## vet: 静态检查
vet:
	$(GO) vet ./...

## fmt: 格式化全仓库
fmt:
	gofmt -w .

## fmtcheck: 仅检查格式（CI 用）
fmtcheck:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "需要格式化:"; echo "$$out"; exit 1; fi

## bench: 运行基准测试
bench:
	$(GO) test -bench=. -benchmem ./...

## clean: 清理构建产物
clean:
	rm -rf dist/
