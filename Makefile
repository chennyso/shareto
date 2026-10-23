VERSION ?= 3.0.0
ARCH ?= amd64
BINARY := dist/shareto

.PHONY: all build test clean deb

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
	ARCH=$(ARCH) VERSION=$(VERSION) bash packaging/build-deb.sh

clean:
	rm -rf dist/
