BINARY    := terraform-provider-mergify
HOSTNAME  := registry.terraform.io
NAMESPACE := Mergifyio
NAME      := mergify
VERSION   := 0.0.1
OS_ARCH   := $(shell go env GOOS)_$(shell go env GOARCH)

PLUGIN_DIR := $(HOME)/.terraform.d/plugins/$(HOSTNAME)/$(NAMESPACE)/$(NAME)/$(VERSION)/$(OS_ARCH)

.PHONY: build install fmt vet test tidy

build:
	go build -o $(BINARY)

install: build
	mkdir -p $(PLUGIN_DIR)
	mv $(BINARY) $(PLUGIN_DIR)/

fmt:
	gofmt -s -w .

vet:
	go vet ./...

test:
	go test ./...

tidy:
	go mod tidy
