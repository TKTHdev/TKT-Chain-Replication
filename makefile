BINARY_NAME  := chain_server
CONFIG_FILE  := cluster.conf
LOG_DIR      := ./logs

ALL_IDS := $(shell jq -r '.[].id' $(CONFIG_FILE))
TARGET_ID ?= all
ifeq ($(TARGET_ID),all)
    IDS := $(ALL_IDS)
else
    IDS := $(TARGET_ID)
endif

DEBUG ?= false
DEBUG_FLAG :=
ifeq ($(DEBUG),true)
    DEBUG_FLAG := --debug
endif

.PHONY: help build start kill clean

help:
	@echo "Usage: make [target] [TARGET_ID=id] [DEBUG=true]"
	@echo "Targets:"
	@echo "  build   - Build the binary"
	@echo "  start   - Start all nodes (or TARGET_ID=n for specific node)"
	@echo "  kill    - Kill all nodes (or TARGET_ID=n for specific node)"
	@echo "  clean   - Remove binaries and logs"

build:
	go build -o $(BINARY_NAME) .

start: build
	@mkdir -p $(LOG_DIR)
	@for id in $(IDS); do \
		echo "Starting node $$id..."; \
		./$(BINARY_NAME) start --id $$id --conf $(CONFIG_FILE) $(DEBUG_FLAG) > $(LOG_DIR)/node_$$id.log 2>&1 & \
		echo $$! > $(LOG_DIR)/node_$$id.pid; \
	done
	@echo "All nodes started."

kill:
	@for id in $(IDS); do \
		if [ -f $(LOG_DIR)/node_$$id.pid ]; then \
			pid=$$(cat $(LOG_DIR)/node_$$id.pid); \
			echo "Killing node $$id (PID: $$pid)..."; \
			kill $$pid 2>/dev/null || echo "Node $$id not running."; \
			rm -f $(LOG_DIR)/node_$$id.pid; \
		else \
			echo "Node $$id: no PID file found."; \
		fi; \
	done

clean:
	rm -f $(BINARY_NAME)
	rm -rf $(LOG_DIR)
