VERSION ?= 3.0.0
ARCH ?= amd64
RPM_ARCH ?= $(ARCH)
ifeq ($(ARCH),amd64)
	RPM_ARCH := x86_64
endif
BINARY := dist/shareto

.PHONY: all build test clean deb rpm tarball package

all: build

build:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) GOTOOLCHAIN=local go build \
		-trimpath \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o $(BINARY) ./cmd/shareto

test:
	GOTOOLCHAIN=local go test -v -race ./...

deb: build
	VERSION=$(VERSION) ARCH=$(ARCH) nfpm pkg --config packaging/nfpm.yaml --packager deb --target dist/shareto_$(VERSION)_$(ARCH).deb

rpm: build
	VERSION=$(VERSION) ARCH=$(RPM_ARCH) nfpm pkg --config packaging/nfpm.yaml --packager rpm --target dist/shareto-$(VERSION)-1.$(RPM_ARCH).rpm

tarball: build
	@mkdir -p dist/tar-staging/shareto
	@cp $(BINARY) dist/tar-staging/shareto/
	@cp packaging/shareto.service dist/tar-staging/shareto/
	@cp README.md dist/tar-staging/shareto/
	@cp LICENSE dist/tar-staging/shareto/
	@tar -czf dist/shareto_$(VERSION)_linux_$(ARCH).tar.gz -C dist/tar-staging shareto
	@rm -rf dist/tar-staging
	@echo "created tarball: dist/shareto_$(VERSION)_linux_$(ARCH).tar.gz"

package: deb rpm tarball

clean:
	rm -rf dist/
