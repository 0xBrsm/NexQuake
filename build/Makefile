SHELL := /usr/bin/env bash

ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)

PLATFORM ?=
OUT_DIR ?= $(ROOT)/build/tmp/bin

.PHONY: all nexus server client clean info

all: nexus server

nexus:
	@$(MAKE) -C "$(ROOT)/nexus" build PLATFORM="$(PLATFORM)" OUT_DIR="$(OUT_DIR)"

server:
	@OUT="$(OUT_DIR)/nqserver" PLATFORM="$(PLATFORM)" "$(ROOT)/build/build-server.sh"

client:
	@OUT_DIR="$(OUT_DIR)/nqwasm" "$(ROOT)/build/build-client.sh"

clean:
	@$(MAKE) -C "$(ROOT)/nexus" clean OUT_DIR="$(OUT_DIR)" || true
	@rm -f "$(OUT_DIR)/nqserver"
	@rm -rf "$(OUT_DIR)/nqwasm"

info:
	@echo "ROOT=$(ROOT)"
	@echo "PLATFORM=$(PLATFORM)"
	@echo "OUT_DIR=$(OUT_DIR)"
