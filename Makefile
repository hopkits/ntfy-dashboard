BINARY  := ntfy-dashboard
PREFIX  ?= /usr/local
BINDIR  := $(PREFIX)/bin
VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)

.PHONY: all build test vet fmt clean install uninstall

all: test build

build:
	VERSION=$(VERSION) ./build.sh

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -rf bin

install: build
	install -m 0755 bin/$(BINARY) $(BINDIR)/$(BINARY)
	@echo "已安装到 $(BINDIR)/$(BINARY)"
	@echo "接下来："
	@echo "  1. install -m 0600 -D configs/dashboard.yml.example /etc/ntfy/dashboard.yml"
	@echo "  2. $(BINDIR)/$(BINARY) -hash-password   # 生成密码哈希并填入配置"
	@echo "  3. install -m 0644 configs/ntfy-dashboard.service /etc/systemd/system/"
	@echo "  4. systemctl daemon-reload && systemctl enable --now ntfy-dashboard"

uninstall:
	rm -f $(BINDIR)/$(BINARY)
	systemctl disable --now ntfy-dashboard 2>/dev/null || true
	rm -f /etc/systemd/system/ntfy-dashboard.service
	systemctl daemon-reload 2>/dev/null || true
