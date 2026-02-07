BINARY_NAME  := chain_server
CONFIG_FILE  := cluster.conf
LOG_DIR      := ./logs
RESULT_DIR   := ./results

ALL_IDS := $(shell jq -r '.[] | select(.role == "server") | .id' $(CONFIG_FILE))
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

# Benchmark parameters
WORKLOAD ?= ycsb-a
WORKERS  ?= 1
WRITE_BATCH ?= 1
TYPE ?= ycsb-a
TIMESTAMP := $(shell date +%Y%m%d_%H%M%S)

.PHONY: help build start kill clean benchmark

help:
	@echo "Usage: make [target] [OPTIONS]"
	@echo ""
	@echo "Targets:"
	@echo "  build     - Build the binary"
	@echo "  start     - Start all nodes (or TARGET_ID=n for specific node)"
	@echo "  kill      - Kill all nodes (or TARGET_ID=n for specific node)"
	@echo "  clean     - Remove binaries and logs"
	@echo "  benchmark - Run YCSB benchmark (CSV output)"
	@echo ""
	@echo "Options:"
	@echo "  DEBUG=true              - Enable debug logging"
	@echo "  TYPE=ycsb-a             - Workload type (ycsb-a, ycsb-b, ycsb-c)"
	@echo "  WORKERS='1 2 4'         - Space-separated worker counts"
	@echo "  WRITE_BATCH='1 2 4 8'   - Space-separated batch sizes"

build:
	go build -o $(BINARY_NAME) .

start: build
	@mkdir -p $(LOG_DIR)
	@for id in $(IDS); do \
		echo "Starting node $$id..."; \
		./$(BINARY_NAME) start --id $$id --conf $(CONFIG_FILE) --write-batch-size $(WRITE_BATCH) $(DEBUG_FLAG) > $(LOG_DIR)/node_$$id.log 2>&1 & \
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
	rm -rf $(RESULT_DIR)

benchmark: build
	@mkdir -p $(RESULT_DIR)
	$(eval CSV_FILE := $(RESULT_DIR)/benchmark_$(TIMESTAMP)_$(TYPE).csv)
	@echo "Workload,WriteBatch,Workers,Throughput(ops/sec),Latency(ms)" > $(CSV_FILE)
	@for wb in $(WRITE_BATCH); do \
		for w in $(WORKERS); do \
			echo "=== WriteBatch=$$wb Workers=$$w ==="; \
			$(MAKE) kill 2>/dev/null || true; \
			sleep 1; \
			mkdir -p $(LOG_DIR); \
			for id in $(ALL_IDS); do \
				./$(BINARY_NAME) start --id $$id --conf $(CONFIG_FILE) --write-batch-size $$wb $(DEBUG_FLAG) > $(LOG_DIR)/node_$$id.log 2>&1 & \
				echo $$! > $(LOG_DIR)/node_$$id.pid; \
			done; \
			sleep 2; \
			./$(BINARY_NAME) client --conf $(CONFIG_FILE) --workload $(TYPE) --workers $$w --write-batch-size $$wb $(DEBUG_FLAG) \
				| tee -a $(LOG_DIR)/bench.log \
				| grep '^RESULT:' \
				| sed 's/^RESULT://' >> $(CSV_FILE); \
			$(MAKE) kill 2>/dev/null || true; \
			sleep 1; \
		done; \
	done
	@echo "Results written to $(CSV_FILE)"
	@cat $(CSV_FILE)
