VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/rhisijmuiden/unify-syslog-server/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildDate=$(DATE)

.PHONY: all web build probe test dist rpi-image clean

all: build

web:
	cd web && npm ci && npm run build && touch dist/.gitkeep

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/unify-syslog ./cmd/unify-syslog

probe:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/unify-syslog-probe ./cmd/unify-syslog-probe

test:
	go test ./...

# Release tarballs + checksums, as consumed by the in-app updater and installer.
dist: web
	rm -rf dist && mkdir -p dist
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/unify-syslog ./cmd/unify-syslog && \
		tar --owner=0 --group=0 --numeric-owner -C dist -czf dist/unify-syslog-server_linux_$$arch.tar.gz unify-syslog && rm dist/unify-syslog; \
	done
	# The probe also runs on 32-bit Raspberry Pi OS (GOARM=6 covers every Pi).
	for arch in amd64 arm64 arm; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch GOARM=6 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/unify-syslog-probe ./cmd/unify-syslog-probe && \
		tar --owner=0 --group=0 --numeric-owner -C dist -czf dist/unify-syslog-probe_linux_$$arch.tar.gz unify-syslog-probe && rm dist/unify-syslog-probe; \
	done
	cd dist && sha256sum *.tar.gz > checksums.txt

# Raspberry Pi image (needs root for loop devices); run after dist.
rpi-image:
	mkdir -p dist/tool && tar -C dist/tool -xzf dist/unify-syslog-probe_linux_$(shell go env GOARCH).tar.gz
	sudo rpi/build-image.sh dist/unify-syslog-probe_linux_arm64.tar.gz dist/tool/unify-syslog-probe dist/unify-syslog-probe-rpi-arm64.img.xz
	rm -rf dist/tool

clean:
	rm -rf bin dist web/dist/assets
