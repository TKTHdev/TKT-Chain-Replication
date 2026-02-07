# Chain Replication

[English](README.en.md)

Chain Replication プロトコルの Go 実装。インメモリ KV ストアをステートマシンとし、YCSB ベンチマークによる性能測定が可能。

## 概要

Chain Replication は線形チェーン状にノードを並べるレプリケーション手法である。

- **書き込み (PUT)**: クライアント → Head → Middle → ... → Tail → クライアントへ ACK
- **読み取り (GET)**: クライアント → Tail → クライアントへ応答

```
Client --PUT--> [Head] --> [Middle] --> ... --> [Tail] --ACK--> Client
Client --GET-------------------------------------> [Tail] --Resp-> Client
```

全ノードが同一の状態を保持するため、Tail から読み取ることで強い一貫性が保証される。

## FIFO 順序保証

ノード間のメッセージ転送は UDP を使用しているが、Chain Replication は FIFO リンクを前提とするため、アプリケーション層で順序保証を実装している。

- Head が各書き込みに単調増加するチェーンシーケンス番号を付与 (`MsgTypeChainForward` エンベロープ)
- 下流ノードはシーケンス番号に基づくリオーダバッファを持ち、到着順によらず Head が決定した順序で適用
- 処理は `chainMu` ミューテックスにより直列化される

## ファイル構成

| ファイル | 説明 |
|---|---|
| `chain.go` | `ChainNode` 構造体の定義、初期化、起動 |
| `conns.go` | UDP 通信、メッセージハンドラ、FIFO リオーダバッファ |
| `message.go` | メッセージのエンコード/デコード、チェーン転送エンベロープ |
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

### ビルド

```bash
make build
```

### サーバ起動

全ノードをバックグラウンドで起動:

```bash
make start
```

特定のノードのみ起動:

```bash
make start TARGET_ID=1
```

デバッグログ有効:

```bash
make start DEBUG=true
```

### サーバ停止

```bash
make kill
```

### 手動起動

```bash
# ノード個別に起動
./chain_server start --id 1 --conf cluster.conf
./chain_server start --id 2 --conf cluster.conf
./chain_server start --id 3 --conf cluster.conf

# クライアント実行
./chain_server client --conf cluster.conf --workload ycsb-a --workers 4
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

YCSB ワークロードによる性能測定:

```bash
# YCSB-A (50% read / 50% write), ワーカー数を変えて測定
make benchmark TYPE=ycsb-a WORKERS='1 2 4 8 16 32 64 128'

# YCSB-B (95% read / 5% write)
make benchmark TYPE=ycsb-b WORKERS='1 2 4 8 16 32 64 128'
```

結果は `results/` ディレクトリに CSV で出力される。

| ワークロード | 読み書き比率 |
|---|---|
| YCSB-A | 50% read / 50% write |
| YCSB-B | 95% read / 5% write |
| YCSB-C | 100% read |

### ベンチマーク結果例

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
go test -v -race ./...
```

- **TestConsistencySequential**: 逐次書き込み後、全ノードの状態が期待値と一致することを検証
- **TestConsistencyConcurrent**: 512 並行ゴルーチンから 2000 回ずつ書き込みを行い、全ノードの状態が一致することを検証 (FIFO 違反の検出)

## 未実装の機能

本実装は Chain Replication の正常系パス (書き込み伝播・読み取り) のみを実装している。原論文 (van Renesse & Schneider, 2004) で定義されている以下の機能は未実装である。

### 障害検知

ノード間のハートビートや障害検知の仕組みがない。ノードがクラッシュしてもチェーン内の他ノードは検知できず、書き込みがサイレントに失われる。

### チェーン再構成

障害ノードの除去や新ノードの追加によるチェーンの動的再構成が実装されていない。原論文ではこれを管理する Master プロセスが定義されている。

- **Head 障害**: 後継ノードを新 Head に昇格させる
- **Tail 障害**: 先行ノードを新 Tail に昇格させる
- **Middle 障害**: 先行ノードの successor と後継ノードの predecessor を更新してチェーンを修復する
- **ノード追加**: 新ノードを Tail の後に追加し、状態転送後にチェーンに組み込む

### 信頼性のある配送

UDP はメッセージのロスを保証しない。チェーンシーケンス番号による順序保証は実装されているが、メッセージが消失した場合のリオーダバッファは永久に後続メッセージをバッファし続ける。再送機構が必要。

### ACK のチェーン逆伝播

原論文では ACK は Tail → ... → Head とチェーンを逆方向に伝播し、Head が保持する Pending リストから確認済みの書き込みを削除する。現在の実装では Tail からクライアントに直接 ACK を送信しており、Head は書き込みの完了を把握できない。これは障害発生時の再送に必要となる。

### 永続化

状態は純粋にインメモリで保持されている。WAL (Write-Ahead Log) やスナップショットの仕組みがないため、プロセスのクラッシュでデータが全て失われる。

### 状態転送

新ノードがチェーンに参加する際、既存ノードから状態を転送して追いつく仕組みがない。全ノードを同時に起動する前提になっている。

### クライアントの再試行

クライアントの PUT/GET は 5 秒のタイムアウトで失敗を返すが、自動的なリトライは行わない。障害発生時にクライアントが Head/Tail のアドレスを更新する仕組みもない。

### CRAQ (読み取り負荷分散)

CRAQ (Chain Replication with Apportioned Queries) では、clean な状態を持つ任意のノードから読み取りが可能で、読み取りスループットがノード数に比例してスケールする。現在の実装では全ての読み取りが Tail に集中する。
