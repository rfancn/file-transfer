VERSION ?= dev
GO ?= go
BINDIR := bin
LDFLAGS := -s -w -X github.com/rfancn/file-transfer/internal/version.Version=$(VERSION)

.PHONY: all build test lint clean

all: build

build:
	mkdir -p $(BINDIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINDIR)/ft-server ./cmd/server
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINDIR)/ft-client ./cmd/client
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINDIR)/ft-admin ./cmd/admin

test:
	$(GO) test -race ./...

lint:
	$(GO) vet ./...

clean:
	rm -rf $(BINDIR) dist