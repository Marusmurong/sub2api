.PHONY: build generate test test-unit test-integration test-e2e

VERSION ?= $(shell ./scripts/resolve-version.sh)
# commit / date 必须一起打进去：线上只有 VERSION 时，`sub2api -version` 输出
# "0.2.3 (commit: unknown, built: unknown)"，无法证明跑的是哪一版源码。
# 2026-09-08 排查账号 216 时就卡在这里——同一晚二进制换过两次、配置改过一次，
# 事后只能靠日志形态倒推当时跑的是修复前还是修复后。
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS ?= -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.Date=$(BUILD_DATE)

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server

test:
	go test ./...
	golangci-lint run ./...

test-unit:
	go test -tags=unit ./...

test-integration:
	go test -tags=integration ./...

test-e2e:
	./scripts/e2e-test.sh

test-e2e-local:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
