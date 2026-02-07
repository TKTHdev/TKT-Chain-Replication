# Chain Replication

[Japanese / 日本語](README.ja.md)

A Go implementation of the Chain Replication protocol with an in-memory KV store as the state machine and YCSB-based benchmarking.

## Overview

Chain Replication arranges replica nodes in a linear chain.

- **Writes (PUT)**: Client -> Head -> Middle -> ... -> Tail -> ACK to Client
- **Reads (GET)**: Client -> Tail -> Response to Client

```
Client --PUT--> [Head] --> [Middle] --> ... --> [Tail] --ACK--> Client
Client --GET-------------------------------------> [Tail] --Resp-> Client
```

All nodes maintain identical state. Reading from the Tail guarantees strong consistency.

## FIFO Ordering Guarantee

Inter-node communication uses UDP, but Chain Replication requires FIFO links. This is enforced at the application layer:

- The Head assigns a monotonically increasing chain sequence number to each write (`MsgTypeChainForward` envelope)
- Downstream nodes maintain a reorder buffer keyed by sequence number, applying writes in the Head's original order regardless of arrival order
- Processing is serialized under a `chainMu` mutex

## File Structure

| File | Description |
|---|---|
| `chain.go` | `ChainNode` struct definition, initialization, startup |
| `conns.go` | UDP communication, message handlers, FIFO reorder buffer |
| `message.go` | Message encoding/decoding, chain forward envelope |
| `client.go` | Benchmark client (YCSB-A/B/C) |
| `config.go` | JSON config file parsing |
| `init.go` | CLI entry point (`urfave/cli`) |
| `chain_test.go` | Replica state consistency tests |
| `cluster.conf` | Cluster configuration (JSON) |
| `makefile` | Build, launch, and benchmark automation |

## Requirements

- Go 1.24+
- `jq` (used by the makefile to extract node IDs)

## Quick Start

### Build

```bash
make build
```

### Start Servers

Start all nodes in the background:

```bash
make start
```

Start a specific node:

```bash
make start TARGET_ID=1
```

Enable debug logging:

```bash
make start DEBUG=true
```

### Stop Servers

```bash
make kill
```

### Manual Startup

```bash
# Start nodes individually
./chain_server start --id 1 --conf cluster.conf
./chain_server start --id 2 --conf cluster.conf
./chain_server start --id 3 --conf cluster.conf

# Run client
./chain_server client --conf cluster.conf --workload ycsb-a --workers 4
```

## Configuration

`cluster.conf` defines nodes as a JSON array. Nodes with `role: "server"` form the chain, ordered by ascending ID (lowest = Head, highest = Tail).

```json
[
  { "id": 0, "ip": "localhost", "port": 4999, "role": "client" },
  { "id": 1, "ip": "localhost", "port": 5000, "role": "server" },
  { "id": 2, "ip": "localhost", "port": 5001, "role": "server" },
  { "id": 3, "ip": "localhost", "port": 5002, "role": "server" }
]
```

## Benchmarking

Run YCSB workloads:

```bash
# YCSB-A (50% read / 50% write), varying worker count
make benchmark TYPE=ycsb-a WORKERS='1 2 4 8 16 32 64 128'

# YCSB-B (95% read / 5% write)
make benchmark TYPE=ycsb-b WORKERS='1 2 4 8 16 32 64 128'
```

Results are written as CSV files to the `results/` directory.

| Workload | Read/Write Ratio |
|---|---|
| YCSB-A | 50% read / 50% write |
| YCSB-B | 95% read / 5% write |
| YCSB-C | 100% read |

### Sample Benchmark Results

3-node chain, YCSB-A (50% write):

| Workers | Throughput (ops/sec) | Latency (ms) |
|---|---|---|
| 1 | 1,998 | 0.50 |
| 8 | 15,411 | 0.52 |
| 32 | 34,537 | 0.92 |
| 128 | 41,856 | 3.05 |

3-node chain, YCSB-B (5% write):

| Workers | Throughput (ops/sec) | Latency (ms) |
|---|---|---|
| 1 | 4,449 | 0.22 |
| 8 | 52,291 | 0.15 |
| 32 | 86,376 | 0.37 |
| 128 | 86,413 | 1.48 |

## Testing

```bash
go test -v -race ./...
```

- **TestConsistencySequential**: Sends sequential writes and verifies all nodes converge to the expected state
- **TestConsistencyConcurrent**: Sends writes from 512 concurrent goroutines (2000 writes each) and verifies all nodes have identical state (detects FIFO violations)

## Unimplemented Features

This implementation covers only the happy-path of Chain Replication (write propagation and reads). The following features defined in the original paper (van Renesse & Schneider, 2004) are not implemented.

### Failure Detection

There is no heartbeat or failure detection mechanism between nodes. If a node crashes, other nodes in the chain cannot detect it and writes are silently lost.

### Chain Reconfiguration

Dynamic chain reconfiguration — removing failed nodes or adding new ones — is not implemented. The original paper defines a Master process that manages this.

- **Head failure**: Promote the successor to new Head
- **Tail failure**: Promote the predecessor to new Tail
- **Middle failure**: Update the predecessor's successor and the successor's predecessor to bridge the gap
- **Node addition**: Append a new node after the Tail, transfer state, then incorporate it into the chain

### Reliable Delivery

UDP does not guarantee message delivery. While chain sequence numbers enforce ordering, if a message is lost the reorder buffer will stall indefinitely waiting for the missing sequence number. A retransmission mechanism is needed.

### ACK Back-Propagation

In the original paper, ACKs propagate backwards through the chain (Tail -> ... -> Head), allowing the Head to remove confirmed writes from its pending list. The current implementation sends ACKs directly from the Tail to the client. The Head has no knowledge of write completion, which is required for retransmission on failure.

### Persistent Storage

State is held purely in memory. There is no WAL (Write-Ahead Log) or snapshotting mechanism. A process crash results in total data loss.

### State Transfer

There is no mechanism for a new node joining the chain to receive a state snapshot from an existing node. All nodes must be started simultaneously from an empty state.

### Client Retry

Client PUT/GET operations return failure after a 5-second timeout but do not automatically retry. There is no mechanism for clients to discover updated Head/Tail addresses after a reconfiguration.

### CRAQ (Read Load Distribution)

CRAQ (Chain Replication with Apportioned Queries) allows reads from any node that holds a clean version, scaling read throughput linearly with the number of nodes. The current implementation directs all reads to the Tail.
