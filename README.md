# casebook

English | [日本語](README.ja.md)

A CLI that converts OpenTelemetry traces of AI agents (JSONL) into evaluation cases (JSONL).
It supports spans that follow the GenAI semantic conventions, as emitted by Strands Agents.

## Installation

Requires Go 1.27 or later. From the repository root:

```sh
go install ./cmd/casebook
```

## Usage

```sh
# File to file
casebook import -i traces.jsonl -o cases.jsonl

# Stdin to stdout
cat traces.jsonl | casebook import > cases.jsonl
```

| Option | Description | Default |
|---|---|---|
| `-i`, `--input` | Input trace file (`-` for stdin) | `-` |
| `-o`, `--output` | Output case file (`-` for stdout) | `-` |

Cases are written to the output; a summary and the reasons for any skips are written to stderr.

```
skipped trace 4bf9... span 00f0...: no input messages found on the invoke_agent span (...)
120 spans -> 8 cases (1 skipped)
```

## Input

A JSONL file with one JSON value per line. Two formats are accepted and may be mixed:

- **Flat spans**: one span per line (`{"traceId": ..., "spanId": ..., "attributes": {...}, "events": [...]}`)
- **OTLP JSON**: one request per line (`{"resourceSpans": [...]}`), as written by the OpenTelemetry Collector file exporter.

Both the legacy GenAI conventions (events such as `gen_ai.user.message`) and the latest ones (attributes such as `gen_ai.input.messages`) are supported.

**Message content must be recorded in the spans.** Invocations whose content is missing or redacted are skipped.

## Output

One case per outermost agent invocation (`invoke_agent`, `invoke_swarm`, `invoke_graph`), one JSON object per line.

| Field | Contents |
|---|---|
| `id` | ID computed from the input and the tool calls (`sha256:...`) |
| `input.messages` | Messages sent to the agent |
| `observed` | What actually happened: tool calls (`tool_calls`), final output, finish reason |
| `expected` | The expectation. A **draft** generated automatically from `observed` |
| `meta` | Source trace and span IDs, agent name, model, duration, token usage, etc. |

### Notes

- **`expected` needs human review.** Freshly imported cases have `"label_origin": "auto-draft"` and `"reviewed": false`. The draft is a copy of the observed behavior, so check and correct it before using it as ground truth.
- **Tool calls with the same `group` may occur in any order.** Calls issued together in one model response, or run in parallel, share a `group` number, and their order within the group does not matter. Calls without a `group` are evaluated in order.
- **Identical cases are merged.** Cases with the same input and the same tool calls get the same ID, and only the first is kept; the rest are skipped. The ID does not change when only the final output or tool results differ.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success (at least one case was written, even if some were skipped) |
| `1` | Runtime error (a file cannot be opened, an input line is invalid, or no cases were produced) |
| `2` | Usage error (unknown command, invalid flag, or unexpected argument) |

If any input line is invalid, casebook reports its line number and exits without writing any cases.
