# Build, test and package galdav. Release archives are static binaries
# (CGO_ENABLED=0) with the version stamped in.
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PLATFORMS ?= linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64 freebsd/amd64
LDFLAGS    = -s -w -X main.version=$(VERSION)

.PHONY: build test release clean

build:
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o galdav ./cmd/galdav

# Needs a MariaDB/MySQL server; see README.md.
test:
	go vet ./...
	go test -p 1 ./...

# dist/galdav_<version>_<os>_<arch>.tar.gz plus dist/SHA256SUMS
release:
	rm -rf dist && mkdir -p dist
	set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		name=galdav_$(VERSION)_$${os}_$${arch}; \
		mkdir -p dist/$$name; \
		GOOS=$$os GOARCH=$$arch GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$$name/galdav ./cmd/galdav; \
		cp -r LICENSE README.md docs deploy dist/$$name/; \
		tar -C dist -czf dist/$$name.tar.gz $$name; \
		rm -rf dist/$$name; \
	done
	cd dist && sha256sum *.tar.gz > SHA256SUMS

clean:
	rm -rf dist galdav
