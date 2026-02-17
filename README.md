# Chain Replication & CRAQ

[Japanese / 日本語](README.ja.md)

Go implementations of Chain Replication and CRAQ (Chain Replication with Apportioned Queries) with in-memory KV stores and YCSB-based benchmarking.

## Implementations

| Directory | Protocol | Read Path | Paper |
|---|---|---|---|
| `basic/` | Chain Replication | Tail only | van Renesse & Schneider, 2004 |
| `craq/` | CRAQ | Any node (clean) / Tail fallback (dirty) | Jeff Terrace & Michael J. Freedman, 2009 |

## Overview

### Chain Replication (basic/)

Arranges replica nodes in a linear chain. Writes flow Head to Tail; reads are served exclusively by the Tail.

```
Client --PUT--> [Head] --> [Middle] --> ... --> [Tail] --ACK--> Client
Client --GET-------------------------------------> [Tail] --Resp-> Client
```

### CRAQ (craq/)

Extends Chain Replication by allowing reads from any node. Each node maintains a version list per key with dirty/clean status.

- **Clean version**: The write has been committed (ACK backpropagated from Tail). Any node can respond immediately.
- **Dirty version**: The write has not yet been committed. The node queries the Tail for the latest committed version before responding.

```
Write:  Client --PUT--> [Head] --> [Middle] --> [Tail] --ACK--> Client
                                                  |
ChainAck:                [Head] <-- [Middle] <-- [Tail]  (mark clean + GC)

Read (clean):  Client --GET--> [Any Node] --Resp--> Client
Read (dirty):  Client --GET--> [Node] --VersionQuery--> [Tail]
                                [Node] <--VersionResp-- [Tail]
                                [Node] --Resp--> Client
```

## FIFO Ordering Guarantee

Both implementations use UDP for inter-node communication but enforce FIFO ordering at the application layer:

- The Head assigns a monotonically increasing chain sequence number to each write (`MsgTypeChainForward` envelope)
- Downstream nodes maintain a reorder buffer keyed by sequence number, applying writes in the Head's original order regardless of arrival order
- Processing is serialized under a `chainMu` mutex

## File Structure

Both `basic/` and `craq/` share the same file layout:

| File | Description |
|---|---|
| `chain.go` | `ChainNode` struct definition, initialization, startup (CRAQ adds `VersionList`) |
| `conns.go` | UDP communication, message handlers, FIFO reorder buffer |
| `message.go` | Message encoding/decoding, chain forward envelope (CRAQ adds `ChainAck`, `VersionQuery/Response`) |
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

Both `basic/` and `craq/` use the same commands. Examples below use `basic/`:

### Build

```bash
cd basic
make build
```

### Start / Stop Servers

```bash
make start              # Start all nodes
make start TARGET_ID=1  # Start specific node
make start DEBUG=true   # Enable debug logging
make kill               # Stop all nodes
```

### Manual Startup

```bash
./chain_server start --id 1 --conf cluster.conf
./chain_server start --id 2 --conf cluster.conf
./chain_server start --id 3 --conf cluster.conf

./chain_server client --conf cluster.conf --workload ycsb-a --workers 4 --keys 128
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

```bash
# YCSB-A (50% read / 50% write)
make benchmark TYPE=ycsb-a WORKERS='1 2 4 8 16 32 64 128' KEYS=128

# YCSB-B (95% read / 5% write)
make benchmark TYPE=ycsb-b WORKERS='1 2 4 8 16 32 64 128' KEYS=128

# YCSB-C (100% read)
make benchmark TYPE=ycsb-c WORKERS='1 2 4 8 16 32 64 128' KEYS=128
```

Results are written as CSV files to the `results/` directory.

| Workload | Read/Write Ratio |
|---|---|
| YCSB-A | 50% read / 50% write |
| YCSB-B | 95% read / 5% write |
| YCSB-C | 100% read |

### Sample Benchmark Results

> Results were measured on a single local machine (all nodes on localhost).

#### Basic Chain Replication

YCSB-A (50% write), varying chain length (3 / 5 / 7 / 11 nodes):

![YCSB-A Benchmark Results](basic/chain_replication_ycsb-a.png)

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
cd basic && go test -v -race ./...
cd craq && go test -v -race ./...
```

## Unimplemented Features

These implementations cover only the happy-path. The following features defined in the original papers are not implemented:

- **Failure Detection**: No heartbeat or failure detection mechanism between nodes
- **Chain Reconfiguration**: No dynamic chain membership changes (requires a Master process)
- **Reliable Delivery**: No retransmission mechanism for lost UDP messages
- **ACK Back-Propagation** (basic only): ACKs go directly from Tail to Client instead of propagating backwards through the chain
- **Persistent Storage**: State is purely in-memory; no WAL or snapshotting
- **State Transfer**: No mechanism for new nodes to receive a state snapshot
- **Client Retry**: No automatic retry or Head/Tail address discovery after reconfiguration
