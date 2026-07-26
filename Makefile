UNAME_S := $(shell uname -s)

ifeq ($(OS),Windows_NT)
PLUGIN_EXT := dll
else ifeq ($(UNAME_S),Darwin)
PLUGIN_EXT := dylib
else
PLUGIN_EXT := so
endif

PLUGIN_NAME := deepseek-reasoning-bridge
BIN_DIR := $(CURDIR)/bin

SOURCES := abi.go cache.go config.go dashboard.go dispatch.go metrics.go request_inject.go signature.go stream_patch.go

.PHONY: build clean test

build: $(BIN_DIR)/$(PLUGIN_NAME).$(PLUGIN_EXT)

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

$(BIN_DIR)/$(PLUGIN_NAME).$(PLUGIN_EXT): $(SOURCES) go.mod | $(BIN_DIR)
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(abspath $@) .
	rm -f $(BIN_DIR)/$(PLUGIN_NAME).h

clean:
	rm -rf $(BIN_DIR)

test:
	go test ./... -v
