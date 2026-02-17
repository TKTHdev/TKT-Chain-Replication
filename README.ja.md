# Chain Replication & CRAQ

[English](README.md)

Chain Replication および CRAQ (Chain Replication with Apportioned Queries) の Go 実装。インメモリ KV ストアをステートマシンとし、YCSB ベンチマークによる性能測定が可能。

## 実装一覧

| ディレクトリ | プロトコル | Read パス | 論文 |
|---|---|---|---|
| `basic/` | Chain Replication | Tail のみ | van Renesse & Schneider, 2004 |
| `craq/` | CRAQ | 任意ノード (clean) / Tail フォールバック (dirty) | Jeff Terrace & Michael J. Freedman, 2009 |

## 概要

### Chain Replication (basic/)

線形チェーン状にノードを並べるレプリケーション手法。書き込みは Head から Tail へ伝播し、読み取りは Tail のみが処理する。

```
Client --PUT--> [Head] --> [Middle] --> ... --> [Tail] --ACK--> Client
Client --GET-------------------------------------> [Tail] --Resp-> Client
```

### CRAQ (craq/)

Chain Replication を拡張し、任意のノードからの読み取りを可能にする。各ノードはキーごとにバージョンリストを保持し、dirty/clean の状態を管理する。

- **Clean バージョン**: 書き込みがコミット済み (Tail からの ACK がチェーンを逆伝播済み)。任意のノードが即座に応答可能。
- **Dirty バージョン**: 書き込みが未コミット。ノードは Tail に最新コミット済みバージョンを問い合わせてから応答する。

```
Write:  Client --PUT--> [Head] --> [Middle] --> [Tail] --ACK--> Client
                                                  |
ChainAck:                [Head] <-- [Middle] <-- [Tail]  (mark clean + GC)

Read (clean):  Client --GET--> [Any Node] --Resp--> Client
Read (dirty):  Client --GET--> [Node] --VersionQuery--> [Tail]
                                [Node] <--VersionResp-- [Tail]
                                [Node] --Resp--> Client
```

## FIFO 順序保証

両実装ともノード間通信に UDP を使用しているが、アプリケーション層で FIFO 順序を保証している:

- Head が各書き込みに単調増加するチェーンシーケンス番号を付与 (`MsgTypeChainForward` エンベロープ)
- 下流ノードはシーケンス番号に基づくリオーダバッファを持ち、到着順によらず Head が決定した順序で適用
- 処理は `chainMu` ミューテックスにより直列化される

## ファイル構成

`basic/` と `craq/` は同一のファイルレイアウトを共有する:

| ファイル | 説明 |
|---|---|
| `chain.go` | `ChainNode` 構造体の定義、初期化、起動 (CRAQ は `VersionList` を追加) |
| `conns.go` | UDP 通信、メッセージハンドラ、FIFO リオーダバッファ |
| `message.go` | メッセージのエンコード/デコード、チェーン転送エンベロープ (CRAQ は `ChainAck`, `VersionQuery/Response` を追加) |
| `client.go` | ベンチマーククライアント (YCSB-A/B/C) |
| `config.go` | JSON 設定ファイルのパース |
| `init.go` | CLI エントリーポイント (`urfave/cli`) |
| `chain_test.go` | レプリカ間の状態一貫性テスト |
| `cluster.conf` | クラスタ設定 (JSON) |
| `makefile` | ビルド・起動・ベンチマーク自動化 |

## 必要環境

- Go 1.24+
- `jq` (makefile のノード ID 抽出に使用)

## クイックスタート

`basic/` と `craq/` で同じコマンドを使用する。以下は `basic/` の例:

### ビルド

```bash
cd basic
make build
```

### サーバ起動・停止

```bash
make start              # 全ノード起動
make start TARGET_ID=1  # 特定ノードのみ起動
make start DEBUG=true   # デバッグログ有効
make kill               # 全ノード停止
```

### 手動起動

```bash
./chain_server start --id 1 --conf cluster.conf
./chain_server start --id 2 --conf cluster.conf
./chain_server start --id 3 --conf cluster.conf

./chain_server client --conf cluster.conf --workload ycsb-a --workers 4 --keys 128
```

## 設定ファイル

`cluster.conf` は JSON 配列でノードを定義する。`role` が `"server"` のノードがチェーンを構成し、ID の昇順で Head → ... → Tail の順に並ぶ。

```json
[
  { "id": 0, "ip": "localhost", "port": 4999, "role": "client" },
  { "id": 1, "ip": "localhost", "port": 5000, "role": "server" },
  { "id": 2, "ip": "localhost", "port": 5001, "role": "server" },
  { "id": 3, "ip": "localhost", "port": 5002, "role": "server" }
]
```

## ベンチマーク

```bash
# YCSB-A (50% read / 50% write)
make benchmark TYPE=ycsb-a WORKERS='1 2 4 8 16 32 64 128' KEYS=128

# YCSB-B (95% read / 5% write)
make benchmark TYPE=ycsb-b WORKERS='1 2 4 8 16 32 64 128' KEYS=128

# YCSB-C (100% read)
make benchmark TYPE=ycsb-c WORKERS='1 2 4 8 16 32 64 128' KEYS=128
```

結果は `results/` ディレクトリに CSV で出力される。

| ワークロード | 読み書き比率 |
|---|---|
| YCSB-A | 50% read / 50% write |
| YCSB-B | 95% read / 5% write |
| YCSB-C | 100% read |

### ベンチマーク結果例

> 筆者のローカルマシン (全ノードを localhost 上で実行) で計測した結果です。

#### Basic Chain Replication

YCSB-A (50% write)、チェーン長を変えて測定 (3 / 5 / 7 / 11 ノード):

![YCSB-A ベンチマーク結果](basic/chain_replication_ycsb-a.png)

3 ノード構成、YCSB-A (50% write):

| Workers | Throughput (ops/sec) | Latency (ms) |
|---|---|---|
| 1 | 1,998 | 0.50 |
| 8 | 15,411 | 0.52 |
| 32 | 34,537 | 0.92 |
| 128 | 41,856 | 3.05 |

3 ノード構成、YCSB-B (5% write):

| Workers | Throughput (ops/sec) | Latency (ms) |
|---|---|---|
| 1 | 4,449 | 0.22 |
| 8 | 52,291 | 0.15 |
| 32 | 86,376 | 0.37 |
| 128 | 86,413 | 1.48 |

## テスト

```bash
cd basic && go test -v -race ./...
cd craq && go test -v -race ./...
```

## 未実装の機能

本実装は正常系パスのみを実装している。原論文で定義されている以下の機能は未実装:

- **障害検知**: ノード間のハートビートや障害検知の仕組みがない
- **チェーン再構成**: 障害ノードの除去や新ノードの追加による動的再構成が未実装 (Master プロセスが必要)
- **信頼性のある配送**: UDP メッセージ消失時の再送機構がない
- **ACK のチェーン逆伝播** (basic のみ): ACK が Tail からクライアントへ直接送信され、チェーンを逆伝播しない
- **永続化**: 状態は純粋にインメモリ。WAL やスナップショットの仕組みがない
- **状態転送**: 新ノードが既存ノードから状態を受け取る仕組みがない
- **クライアントの再試行**: 自動リトライや Head/Tail アドレスの再発見機構がない
