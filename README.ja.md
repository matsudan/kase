# kase

[English](README.md) | 日本語

AI エージェントの OpenTelemetry トレース（JSONL）を、評価用ケース（JSONL）に変換する CLI です。
Strands Agents が出力する GenAI セマンティック規約のスパンに対応しています。

## 使い方

```sh
# ファイルからファイルへ
kase import -i traces.jsonl -o cases.jsonl

# 標準入力から標準出力へ
cat traces.jsonl | kase import > cases.jsonl
```

| オプション | 説明 | 既定値 |
|---|---|---|
| `-i`, `--input` | 入力トレースファイル（`-` で標準入力） | `-` |
| `-o`, `--output` | 出力ケースファイル（`-` で標準出力） | `-` |

ケースは出力先に、処理結果の要約とスキップ理由は標準エラーに出力されます。

```
skipped trace 4bf9... span 00f0...: no input messages found on the invoke_agent span (...)
120 spans -> 8 cases (1 skipped)
```

## インストール

```sh
go install github.com/matsudan/kase/cmd/kase@latest
```

対応下限バージョンは [`go.mod`](go.mod) の `go` 行を参照してください。

## 入力

1 行に 1 つの JSON を置いた JSONL ファイルです。次の 2 形式を受け付け、混在しても構いません。

- **フラットなスパン**: 1 行 1 スパン（`{"traceId": ..., "spanId": ..., "attributes": {...}, "events": [...]}`）
- **OTLP JSON**: 1 行 1 リクエスト（`{"resourceSpans": [...]}`）。OpenTelemetry Collector の file exporter の出力形式です。

GenAI 規約は legacy（`gen_ai.user.message` などのイベント）と latest（`gen_ai.input.messages` などの属性）の両方に対応しています。

**メッセージ本文がスパンに記録されている必要があります。** 本文が無い、またはマスクされている呼び出しはスキップされます。

## 出力

最も外側のエージェント呼び出し（`invoke_agent`、`invoke_swarm`、`invoke_graph`）ごとに 1 ケースを、1 行 1 JSON で出力します。

| フィールド | 内容 |
|---|---|
| `id` | 入力とツール呼び出しの内容から計算した ID（`sha256:...`） |
| `input.messages` | エージェントへの入力メッセージ |
| `observed` | 実際の実行結果: ツール呼び出し（`tool_calls`）、最終出力、終了理由 |
| `expected` | 期待値。observed から自動生成した**下書き**です |
| `meta` | 元のトレース ID・スパン ID、エージェント名、モデル、所要時間、トークン数など |

### 注意点

- **`expected` は人のレビューが必要です。** 自動生成直後は `"label_origin": "auto-draft"`、`"reviewed": false` になっています。観測された動作をそのまま写したものなので、正解として使う前に内容を確認・修正してください。
- **`group` が同じツール呼び出しは順不同です。** 1 回のモデル応答でまとめて発行された、または並列に実行された呼び出しには同じ `group` 番号が付き、その中の順序は問いません。`group` が無い呼び出しは順序どおりに評価されます。
- **同じケースは 1 件にまとめられます。** 入力とツール呼び出しが同じであれば ID も同じになり、2 件目以降はスキップされます。最終出力やツールの結果が違っていても ID は変わりません。

## 終了コード

| コード | 意味 |
|---|---|
| `0` | 成功（一部スキップがあっても 1 件以上出力できれば成功） |
| `1` | 実行時エラー（ファイルが開けない、不正な入力行がある、ケースが 1 件も生成されない） |
| `2` | 使い方の誤り（不明なコマンド、不正なフラグ、余分な引数） |

入力に不正な行が 1 行でもあると、その行番号を表示して終了します（ケースは出力されません）。
