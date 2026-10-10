package importer

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"casebook/evalcase"
	"casebook/trace"
)

func TestFromSpans(t *testing.T) {
	tests := []struct {
		name string
		// Exactly one of file or lines provides the trace JSONL.
		file  string
		lines []string
		// want is compared field by field except ID, which is checked against
		// ComputeID instead.
		want []evalcase.Case
		// wantSkipped matches TraceID and SpanID exactly and Reason by substring.
		wantSkipped []Skip
	}{
		{
			name: "strands legacy fixture",
			file: "../testdata/strands_legacy.jsonl",
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput("明日の東京の天気を教えて"),
				Observed: evalcase.Observed{
					ToolCalls: []evalcase.ToolCall{{
						ID:         "tooluse_1",
						Name:       "get_weather",
						Agent:      "TravelAgent",
						Args:       map[string]any{"city": "Tokyo", "days": json.Number("1")},
						Result:     []any{map[string]any{"text": "sunny, 24C"}},
						DurationMs: 400,
					}},
					FinalOutput:  "明日の東京は晴れで24度です。",
					FinishReason: "end_turn",
				},
				Expected: draft(evalcase.ExpectedCall{
					Tool: "get_weather",
					Args: map[string]any{"city": "Tokyo", "days": json.Number("1")},
				}),
				Meta: evalcase.Meta{
					Source:     source("aaaa0000000000000000000000000001", "0000000000000002"),
					Agent:      "TravelAgent",
					Model:      "us.anthropic.claude-sonnet-4-20250514-v1:0",
					StartedAt:  "2026-04-23T07:37:44.011990383Z",
					DurationMs: 5622,
					Outcome:    "ok",
					Usage:      &evalcase.Usage{InputTokens: 983, OutputTokens: 232, TotalTokens: 1215},
				},
			}},
		},
		{
			// refund appears first in the file but started later, so tool calls
			// must be ordered by start time, not input order.
			name: "otlp latest-convention fixture",
			file: "../testdata/otlp_latest.jsonl",
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput("Refund order 1234"),
				Observed: evalcase.Observed{
					ToolCalls: []evalcase.ToolCall{
						{
							ID:         "call_1",
							Name:       "get_order",
							Agent:      "RefundAgent",
							Args:       map[string]any{"order_id": "1234"},
							Result:     map[string]any{"id": "1234", "total": json.Number("10.5")},
							DurationMs: 200,
						},
						{
							ID:         "call_2",
							Name:       "refund",
							Agent:      "RefundAgent",
							Args:       map[string]any{"order_id": "1234", "amount": json.Number("10.5")},
							Result:     map[string]any{"error": "timeout"},
							Error:      true,
							DurationMs: 100,
						},
					},
					FinalOutput:  "Sorry, the refund failed.",
					FinishReason: "end_turn",
				},
				Expected: draft(
					evalcase.ExpectedCall{Tool: "get_order", Args: map[string]any{"order_id": "1234"}},
					evalcase.ExpectedCall{Tool: "refund", Args: map[string]any{"order_id": "1234", "amount": json.Number("10.5")}},
				),
				Meta: evalcase.Meta{
					Source:     source("bbbb0000000000000000000000000002", "00000000000000a1"),
					Agent:      "RefundAgent",
					Model:      "claude-sonnet",
					StartedAt:  "2026-04-23T07:37:44Z",
					DurationMs: 4000,
					Outcome:    "ok",
					Usage:      &evalcase.Usage{InputTokens: 120, OutputTokens: 30, TotalTokens: 150},
				},
			}},
		},
		{
			name: "trace without invocation span is skipped",
			lines: []string{
				`{"traceId":"t1","spanId":"s1","name":"execute_tool x","attributes":{"gen_ai.operation.name":"execute_tool"}}`,
			},
			wantSkipped: []Skip{{TraceID: "t1", Reason: "no invoke_* span"}},
		},
		{
			name: "invocation without input is skipped",
			lines: []string{
				`{"traceId":"t2","spanId":"s1","name":"invoke_agent A","attributes":{"gen_ai.operation.name":"invoke_agent"}}`,
			},
			wantSkipped: []Skip{{TraceID: "t2", SpanID: "s1", Reason: "no input messages"}},
		},
		{
			name: "nested agents use the outermost as root",
			lines: []string{
				`{"traceId":"t","spanId":"inner","parentSpanId":"outer","name":"invoke_agent Inner","startTimeUnixNano":2,"attributes":{"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"Inner","gen_ai.input.messages":"[{\"role\":\"user\",\"parts\":[{\"type\":\"text\",\"content\":\"inner q\"}]}]"}}`,
				`{"traceId":"t","spanId":"tool","parentSpanId":"inner","name":"execute_tool search","startTimeUnixNano":3,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"search","gen_ai.tool.call.arguments":"{\"q\":1}"}}`,
				`{"traceId":"t","spanId":"outer","name":"invoke_agent Outer","startTimeUnixNano":1,"attributes":{"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"Outer","gen_ai.input.messages":"[{\"role\":\"user\",\"parts\":[{\"type\":\"text\",\"content\":\"outer q\"}]}]"}}`,
			},
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput("outer q"),
				Observed: evalcase.Observed{ToolCalls: []evalcase.ToolCall{
					// The tool is attributed to the nearest enclosing agent.
					{Name: "search", Agent: "Inner", Args: map[string]any{"q": json.Number("1")}},
				}},
				Expected: draft(evalcase.ExpectedCall{Tool: "search", Args: map[string]any{"q": json.Number("1")}}),
				Meta: evalcase.Meta{
					Source:    source("t", "outer"),
					Agent:     "Outer",
					StartedAt: "1970-01-01T00:00:00.000000001Z",
					Outcome:   "ok",
				},
			}},
		},
		{
			// Strands with OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
			// (and without gen_ai_span_attributes_only) records messages as
			// attributes of gen_ai.client.inference.operation.details events.
			name: "latest convention in event mode",
			lines: []string{
				`{"traceId":"t","spanId":"a","name":"invoke_agent A","startTimeUnixNano":1,"attributes":{"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"A"},"events":[` +
					`{"name":"gen_ai.client.inference.operation.details","attributes":{"gen_ai.input.messages":"[{\"role\":\"user\",\"parts\":[{\"type\":\"text\",\"content\":\"q\"}]}]"}},` +
					`{"name":"gen_ai.client.inference.operation.details","attributes":{"gen_ai.output.messages":"[{\"role\":\"assistant\",\"parts\":[{\"type\":\"text\",\"content\":\"ans\"}],\"finish_reason\":\"end_turn\"}]"}}]}`,
				`{"traceId":"t","spanId":"b","parentSpanId":"a","name":"execute_tool refund","startTimeUnixNano":2,"status":{"code":"STATUS_CODE_ERROR"},"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"refund","gen_ai.tool.call.id":"c1","gen_ai.tool.call.arguments":"{\"id\":1}","gen_ai.tool.status":"error"},"events":[` +
					`{"name":"gen_ai.client.inference.operation.details","attributes":{"gen_ai.output.messages":"[{\"role\":\"tool\",\"parts\":[{\"type\":\"tool_call_response\",\"id\":\"c1\",\"response\":[{\"text\":\"timeout\"}]}]}]"}}]}`,
			},
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput("q"),
				Observed: evalcase.Observed{
					ToolCalls: []evalcase.ToolCall{{
						ID:    "c1",
						Name:  "refund",
						Agent: "A",
						Args:  map[string]any{"id": json.Number("1")},
						// Failed tools have no gen_ai.tool.call.result; the
						// response comes from the tool_call_response part.
						Result: []any{map[string]any{"text": "timeout"}},
						Error:  true,
					}},
					FinalOutput:  "ans",
					FinishReason: "end_turn",
				},
				Expected: draft(evalcase.ExpectedCall{Tool: "refund", Args: map[string]any{"id": json.Number("1")}}),
				Meta: evalcase.Meta{
					Source:    source("t", "a"),
					Agent:     "A",
					StartedAt: "1970-01-01T00:00:00.000000001Z",
					Outcome:   "ok",
				},
			}},
		},
		{
			// Legacy Strands emits one event per history message. Tool-use-only
			// assistant messages must not leak as raw JSON, and the system prompt
			// is agent config, not case input.
			name: "legacy history keeps only text",
			lines: []string{
				`{"traceId":"t","spanId":"a","name":"invoke_agent A","attributes":{"gen_ai.operation.name":"invoke_agent"},"events":[` +
					`{"name":"gen_ai.system.message","attributes":{"content":"[{\"text\": \"you are a bot\"}]"}},` +
					`{"name":"gen_ai.user.message","attributes":{"content":"[{\"text\": \"q\"}]"}},` +
					`{"name":"gen_ai.assistant.message","attributes":{"content":"[{\"toolUse\": {\"name\": \"x\", \"input\": {}, \"toolUseId\": \"1\"}}]"}},` +
					`{"name":"gen_ai.tool.message","attributes":{"content":"[{\"toolResult\": {\"toolUseId\": \"1\", \"content\": [{\"text\": \"r\"}]}}]"}}]}`,
			},
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput("q"),
				// Empty, not nil, so that it serialises as [] rather than null.
				Observed: evalcase.Observed{ToolCalls: []evalcase.ToolCall{}},
				Expected: draft(),
				Meta:     evalcase.Meta{Source: source("t", "a"), Outcome: "ok"},
			}},
		},
		{
			name: "multiple invocations in one trace yield one case each",
			lines: []string{
				`{"traceId":"t","spanId":"p","name":"handler"}`,
				`{"traceId":"t","spanId":"r1","parentSpanId":"p","name":"invoke_agent A","startTimeUnixNano":1,"attributes":{"gen_ai.operation.name":"invoke_agent"},"events":[{"name":"gen_ai.user.message","attributes":{"content":"[{\"text\": \"q1\"}]"}}]}`,
				`{"traceId":"t","spanId":"t1","parentSpanId":"r1","name":"execute_tool x","startTimeUnixNano":2,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"x"}}`,
				`{"traceId":"t","spanId":"r2","parentSpanId":"p","name":"invoke_agent A","startTimeUnixNano":3,"attributes":{"gen_ai.operation.name":"invoke_agent"},"events":[{"name":"gen_ai.user.message","attributes":{"content":"[{\"text\": \"q2\"}]"}}]}`,
				`{"traceId":"t","spanId":"t2","parentSpanId":"r2","name":"execute_tool y","startTimeUnixNano":4,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"y"}}`,
			},
			want: []evalcase.Case{
				{
					SchemaVersion: evalcase.SchemaVersion,
					Input:         userInput("q1"),
					Observed:      evalcase.Observed{ToolCalls: []evalcase.ToolCall{{Name: "x"}}},
					Expected:      draft(evalcase.ExpectedCall{Tool: "x"}),
					Meta: evalcase.Meta{
						Source:    source("t", "r1"),
						StartedAt: "1970-01-01T00:00:00.000000001Z",
						Outcome:   "ok",
					},
				},
				{
					SchemaVersion: evalcase.SchemaVersion,
					Input:         userInput("q2"),
					Observed:      evalcase.Observed{ToolCalls: []evalcase.ToolCall{{Name: "y"}}},
					Expected:      draft(evalcase.ExpectedCall{Tool: "y"}),
					Meta: evalcase.Meta{
						Source:    source("t", "r2"),
						StartedAt: "1970-01-01T00:00:00.000000003Z",
						Outcome:   "ok",
					},
				},
			},
		},
		{
			// Strands Swarm/Graph wrap node agents in an invoke_swarm /
			// invoke_graph span. The case is the whole multi-agent run.
			name: "multi-agent run is a single case",
			lines: []string{
				`{"traceId":"t","spanId":"s","name":"invoke_swarm","startTimeUnixNano":1,"attributes":{"gen_ai.operation.name":"invoke_swarm"},"events":[{"name":"gen_ai.user.message","attributes":{"content":"plan a trip"}},{"name":"gen_ai.choice","attributes":{"message":"final plan"}}]}`,
				`{"traceId":"t","spanId":"n1","parentSpanId":"s","name":"invoke_agent Planner","startTimeUnixNano":2,"attributes":{"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"Planner"},"events":[{"name":"gen_ai.user.message","attributes":{"content":"[{\"text\": \"node input\"}]"}},{"name":"gen_ai.choice","attributes":{"message":"node output"}}]}`,
				`{"traceId":"t","spanId":"t1","parentSpanId":"n1","name":"execute_tool search","startTimeUnixNano":3,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"search"}}`,
				`{"traceId":"t","spanId":"n2","parentSpanId":"s","name":"invoke_agent Booker","startTimeUnixNano":4,"attributes":{"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"Booker"}}`,
				`{"traceId":"t","spanId":"t2","parentSpanId":"n2","name":"execute_tool book","startTimeUnixNano":5,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"book"}}`,
			},
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput("plan a trip"),
				Observed: evalcase.Observed{
					ToolCalls: []evalcase.ToolCall{
						{Name: "search", Agent: "Planner"},
						{Name: "book", Agent: "Booker"},
					},
					FinalOutput: "final plan",
				},
				Expected: draft(evalcase.ExpectedCall{Tool: "search"}, evalcase.ExpectedCall{Tool: "book"}),
				Meta: evalcase.Meta{
					Source:    source("t", "s"),
					StartedAt: "1970-01-01T00:00:00.000000001Z",
					Outcome:   "ok",
				},
			}},
		},
		{
			// JSON-looking text is content, not serialised blocks.
			name: "structured output and json input are kept verbatim",
			lines: []string{
				`{"traceId":"t","spanId":"a","name":"invoke_agent A","attributes":{"gen_ai.operation.name":"invoke_agent",` +
					`"gen_ai.input.messages":"[{\"role\":\"user\",\"parts\":[{\"type\":\"text\",\"content\":\"{\\\"order_id\\\": 1234}\"}]}]",` +
					`"gen_ai.output.messages":"[{\"role\":\"assistant\",\"parts\":[{\"type\":\"text\",\"content\":\"{\\\"answer\\\": 42}\"}]}]"}}`,
			},
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput(`{"order_id": 1234}`),
				Observed:      evalcase.Observed{ToolCalls: []evalcase.ToolCall{}, FinalOutput: `{"answer": 42}`},
				Expected:      draft(),
				Meta:          evalcase.Meta{Source: source("t", "a"), Outcome: "ok"},
			}},
		},
		{
			name: "legacy json text that is not content blocks is kept verbatim",
			lines: []string{
				`{"traceId":"t","spanId":"a","name":"invoke_agent A","attributes":{"gen_ai.operation.name":"invoke_agent"},"events":[` +
					`{"name":"gen_ai.user.message","attributes":{"content":"[1, 2, 3]"}},` +
					`{"name":"gen_ai.choice","attributes":{"message":"{\"answer\": 42}"}}]}`,
			},
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput("[1, 2, 3]"),
				Observed:      evalcase.Observed{ToolCalls: []evalcase.ToolCall{}, FinalOutput: `{"answer": 42}`},
				Expected:      draft(),
				Meta:          evalcase.Meta{Source: source("t", "a"), Outcome: "ok"},
			}},
		},
		{
			// Strands parents tool spans to the event-loop cycle span. Tools of
			// one turn form a group in canonical order (not start order); a
			// tool in a later turn stays ordered after them.
			name: "tools from one model turn are grouped",
			lines: []string{
				`{"traceId":"t","spanId":"a","name":"invoke_agent A","startTimeUnixNano":1,"attributes":{"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"A"},"events":[{"name":"gen_ai.user.message","attributes":{"content":"q"}}]}`,
				`{"traceId":"t","spanId":"c1","parentSpanId":"a","name":"execute_event_loop_cycle","startTimeUnixNano":2}`,
				`{"traceId":"t","spanId":"x1","parentSpanId":"c1","name":"execute_tool search","startTimeUnixNano":3,"endTimeUnixNano":4,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"search","gen_ai.tool.call.arguments":"{\"q\":\"b\"}"}}`,
				`{"traceId":"t","spanId":"x2","parentSpanId":"c1","name":"execute_tool search","startTimeUnixNano":5,"endTimeUnixNano":6,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"search","gen_ai.tool.call.arguments":"{\"q\":\"a\"}"}}`,
				`{"traceId":"t","spanId":"x3","parentSpanId":"c1","name":"execute_tool lookup","startTimeUnixNano":7,"endTimeUnixNano":8,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"lookup"}}`,
				`{"traceId":"t","spanId":"c2","parentSpanId":"a","name":"execute_event_loop_cycle","startTimeUnixNano":10}`,
				`{"traceId":"t","spanId":"x4","parentSpanId":"c2","name":"execute_tool book","startTimeUnixNano":11,"endTimeUnixNano":12,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"book"}}`,
			},
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput("q"),
				Observed: evalcase.Observed{ToolCalls: []evalcase.ToolCall{
					{Name: "lookup", Agent: "A", Group: 1},
					{Name: "search", Agent: "A", Args: map[string]any{"q": "a"}, Group: 1},
					{Name: "search", Agent: "A", Args: map[string]any{"q": "b"}, Group: 1},
					{Name: "book", Agent: "A"},
				}},
				Expected: draft(
					evalcase.ExpectedCall{Tool: "lookup", Group: 1},
					evalcase.ExpectedCall{Tool: "search", Args: map[string]any{"q": "a"}, Group: 1},
					evalcase.ExpectedCall{Tool: "search", Args: map[string]any{"q": "b"}, Group: 1},
					evalcase.ExpectedCall{Tool: "book"},
				),
				Meta: evalcase.Meta{
					Source:    source("t", "a"),
					Agent:     "A",
					StartedAt: "1970-01-01T00:00:00.000000001Z",
					Outcome:   "ok",
				},
			}},
		},
		{
			name: "overlapping tools without a per-turn span are grouped",
			lines: []string{
				`{"traceId":"t","spanId":"a","name":"invoke_agent A","attributes":{"gen_ai.operation.name":"invoke_agent"},"events":[{"name":"gen_ai.user.message","attributes":{"content":"q"}}]}`,
				`{"traceId":"t","spanId":"y","parentSpanId":"a","name":"execute_tool y","startTimeUnixNano":2,"endTimeUnixNano":10,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"y"}}`,
				`{"traceId":"t","spanId":"x","parentSpanId":"a","name":"execute_tool x","startTimeUnixNano":5,"endTimeUnixNano":6,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"x"}}`,
				`{"traceId":"t","spanId":"z","parentSpanId":"a","name":"execute_tool z","startTimeUnixNano":10,"endTimeUnixNano":11,"attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"z"}}`,
			},
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput("q"),
				Observed: evalcase.Observed{ToolCalls: []evalcase.ToolCall{
					{Name: "x", Group: 1},
					{Name: "y", Group: 1},
					{Name: "z"}, // starts exactly when y ends: sequential
				}},
				Expected: draft(
					evalcase.ExpectedCall{Tool: "x", Group: 1},
					evalcase.ExpectedCall{Tool: "y", Group: 1},
					evalcase.ExpectedCall{Tool: "z"},
				),
				Meta: evalcase.Meta{Source: source("t", "a"), Outcome: "ok"},
			}},
		},
		{
			name: "duplicate cases across traces are reported, first wins",
			lines: []string{
				`{"traceId":"t1","spanId":"a","name":"invoke_agent A","attributes":{"gen_ai.operation.name":"invoke_agent"},"events":[{"name":"gen_ai.user.message","attributes":{"content":"q"}},{"name":"gen_ai.choice","attributes":{"message":"first"}}]}`,
				`{"traceId":"t2","spanId":"b","name":"invoke_agent A","attributes":{"gen_ai.operation.name":"invoke_agent"},"events":[{"name":"gen_ai.user.message","attributes":{"content":"q"}},{"name":"gen_ai.choice","attributes":{"message":"second"}}]}`,
			},
			want: []evalcase.Case{{
				SchemaVersion: evalcase.SchemaVersion,
				Input:         userInput("q"),
				Observed:      evalcase.Observed{ToolCalls: []evalcase.ToolCall{}, FinalOutput: "first"},
				Expected:      draft(),
				Meta:          evalcase.Meta{Source: source("t1", "a"), Outcome: "ok"},
			}},
			wantSkipped: []Skip{{TraceID: "t2", SpanID: "b", Reason: "duplicate of case sha256:"}},
		},
		{
			name: "same tool called by different agents is not a duplicate",
			lines: []string{
				`{"traceId":"t1","spanId":"a","name":"invoke_agent P","attributes":{"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"P"},"events":[{"name":"gen_ai.user.message","attributes":{"content":"q"}}]}`,
				`{"traceId":"t1","spanId":"x","parentSpanId":"a","name":"execute_tool s","attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"s"}}`,
				`{"traceId":"t2","spanId":"b","name":"invoke_agent Q","attributes":{"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"Q"},"events":[{"name":"gen_ai.user.message","attributes":{"content":"q"}}]}`,
				`{"traceId":"t2","spanId":"y","parentSpanId":"b","name":"execute_tool s","attributes":{"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"s"}}`,
			},
			want: []evalcase.Case{
				{
					SchemaVersion: evalcase.SchemaVersion,
					Input:         userInput("q"),
					Observed:      evalcase.Observed{ToolCalls: []evalcase.ToolCall{{Name: "s", Agent: "P"}}},
					Expected:      draft(evalcase.ExpectedCall{Tool: "s"}),
					Meta:          evalcase.Meta{Source: source("t1", "a"), Agent: "P", Outcome: "ok"},
				},
				{
					SchemaVersion: evalcase.SchemaVersion,
					Input:         userInput("q"),
					Observed:      evalcase.Observed{ToolCalls: []evalcase.ToolCall{{Name: "s", Agent: "Q"}}},
					Expected:      draft(evalcase.ExpectedCall{Tool: "s"}),
					Meta:          evalcase.Meta{Source: source("t2", "b"), Agent: "Q", Outcome: "ok"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromSpans(readSpans(t, tt.file, tt.lines))

			if len(got.Skipped) != len(tt.wantSkipped) {
				t.Fatalf("Skipped = %v, want %v", got.Skipped, tt.wantSkipped)
			}
			for i, s := range got.Skipped {
				w := tt.wantSkipped[i]
				if s.TraceID != w.TraceID || s.SpanID != w.SpanID || !strings.Contains(s.Reason, w.Reason) {
					t.Errorf("Skipped[%d] = %+v, want %+v (reason as substring)", i, s, w)
				}
			}

			if len(got.Cases) != len(tt.want) {
				t.Fatalf("got %d cases, want %d:\n%s", len(got.Cases), len(tt.want), dump(got.Cases))
			}
			for i := range got.Cases {
				c := got.Cases[i]
				wantID, err := c.ComputeID()
				if err != nil {
					t.Fatal(err)
				}
				if c.ID != wantID {
					t.Errorf("Cases[%d].ID = %q, want ComputeID() = %q", i, c.ID, wantID)
				}
				c.ID = ""
				if !reflect.DeepEqual(c, tt.want[i]) {
					t.Errorf("Cases[%d] mismatch\n got: %s\nwant: %s", i, dump(c), dump(tt.want[i]))
				}
			}
		})
	}
}

func TestExtractText(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "plain", in: "hello", want: "hello"},
		{name: "empty string", in: "", want: ""},
		{name: "json-looking but not json", in: "[1] hello", want: "[1] hello"},
		{name: "tool-only blocks", in: `[{"toolUse": {"name": "x"}}]`, want: ""},
		{name: "strands blocks", in: `[{"text": "a"}, {"text": "b"}]`, want: "a\nb"},
		{name: "strands tool use ignored", in: `[{"toolUse": {"name": "x"}}, {"text": "a"}]`, want: "a"},
		{name: "strands image block ignored", in: `[{"image": {"format": "png"}}, {"text": "caption"}]`, want: "caption"},
		{name: "strands reasoning block ignored", in: `[{"reasoningContent": {"reasoningText": {"text": "hmm"}}}]`, want: ""},

		// Valid JSON that is not a content-block list is text, not structure.
		{name: "json object text", in: `{"answer": 42}`, want: `{"answer": 42}`},
		{name: "json object with text key", in: `{"text": "a"}`, want: `{"text": "a"}`},
		{name: "json array of numbers", in: `[1, 2, 3]`, want: `[1, 2, 3]`},
		{name: "json array of strings", in: `["a", "b"]`, want: `["a", "b"]`},
		{name: "empty json array", in: `[]`, want: `[]`},
		{name: "unknown block key", in: `[{"text": "a", "extra": 1}]`, want: `[{"text": "a", "extra": 1}]`},
		{name: "empty object in list", in: `[{}]`, want: `[{}]`},

		{
			name: "genai parts",
			in:   []any{map[string]any{"type": "text", "content": "x"}, map[string]any{"type": "tool_call", "name": "t"}},
			want: "x",
		},
		{
			name: "genai part content is literal even if it looks like blocks",
			in:   []any{map[string]any{"type": "text", "content": `[{"text": "a"}]`}},
			want: `[{"text": "a"}]`,
		},
		{name: "reasoning ignored", in: []any{map[string]any{"type": "reasoning", "content": "think"}}, want: ""},
		{name: "nested message", in: map[string]any{"message": map[string]any{"content": "deep"}}, want: "deep"},
		{name: "number", in: json.Number("1"), want: ""},
		{name: "nil", in: nil, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractText(tt.in); got != tt.want {
				t.Errorf("extractText(%#v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseMaybeJSON(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want any
	}{
		{name: "object", in: `{"a":1}`, want: map[string]any{"a": json.Number("1")}},
		{name: "array with surrounding space", in: " [1, \"x\"] ", want: []any{json.Number("1"), "x"}},
		{name: "plain string", in: "hello", want: "hello"},
		{name: "json scalar string kept", in: `"quoted"`, want: `"quoted"`},
		{name: "invalid json kept", in: `{oops`, want: `{oops`},
		{name: "trailing data kept", in: `{"a":1} {"b":2}`, want: `{"a":1} {"b":2}`},
		{name: "non-string unchanged", in: json.Number("5"), want: json.Number("5")},
		{name: "nil", in: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseMaybeJSON(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseMaybeJSON(%#v) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestConcurrencyGroups(t *testing.T) {
	invoke := trace.Span{SpanID: "inv", Attributes: trace.Attrs{"gen_ai.operation.name": "invoke_agent"}}
	cycle1 := trace.Span{SpanID: "c1", ParentSpanID: "inv", Name: "execute_event_loop_cycle"}
	cycle2 := trace.Span{SpanID: "c2", ParentSpanID: "inv", Name: "execute_event_loop_cycle"}
	byID := map[string]trace.Span{"inv": invoke, "c1": cycle1, "c2": cycle2}

	tool := func(parent string, start, end int64) trace.Span {
		return trace.Span{ParentSpanID: parent, Start: start, End: end}
	}
	tests := []struct {
		name  string
		tools []trace.Span // sorted by start, as buildCase passes them
		want  []int
	}{
		{name: "none", want: []int{}},
		{name: "single", tools: []trace.Span{tool("c1", 1, 2)}, want: []int{0}},
		{
			name:  "same cycle, sequential in time",
			tools: []trace.Span{tool("c1", 1, 2), tool("c1", 3, 4)},
			want:  []int{1, 1},
		},
		{
			name:  "different cycles, sequential",
			tools: []trace.Span{tool("c1", 1, 2), tool("c2", 3, 4)},
			want:  []int{0, 0},
		},
		{
			name:  "direct children of the invocation, sequential",
			tools: []trace.Span{tool("inv", 1, 2), tool("inv", 3, 4)},
			want:  []int{0, 0},
		},
		{
			name:  "direct children of the invocation, overlapping",
			tools: []trace.Span{tool("inv", 1, 5), tool("inv", 3, 4)},
			want:  []int{1, 1},
		},
		{
			name:  "touching intervals are sequential",
			tools: []trace.Span{tool("inv", 1, 3), tool("inv", 3, 4)},
			want:  []int{0, 0},
		},
		{
			// The third span overlaps the first, not the second.
			name:  "overlap is measured against the whole batch",
			tools: []trace.Span{tool("inv", 1, 10), tool("inv", 2, 3), tool("inv", 5, 6)},
			want:  []int{1, 1, 1},
		},
		{
			name:  "overlap across cycles",
			tools: []trace.Span{tool("c1", 1, 5), tool("c2", 3, 4)},
			want:  []int{1, 1},
		},
		{
			name:  "unknown timestamps do not overlap",
			tools: []trace.Span{tool("inv", 0, 0), tool("inv", 0, 0)},
			want:  []int{0, 0},
		},
		{
			name:  "missing parent is not a turn",
			tools: []trace.Span{tool("gone", 1, 2), tool("gone", 3, 4)},
			want:  []int{0, 0},
		},
		{
			name: "groups are numbered in order",
			tools: []trace.Span{
				tool("c1", 1, 2), tool("c1", 2, 3),
				tool("inv", 10, 11),
				tool("c2", 20, 21), tool("c2", 22, 23),
			},
			want: []int{1, 1, 0, 2, 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := concurrencyGroups(tt.tools, byID); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("concurrencyGroups() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSkipString(t *testing.T) {
	tests := []struct {
		name string
		in   Skip
		want string
	}{
		{name: "trace-level", in: Skip{TraceID: "t", Reason: "r"}, want: "trace t: r"},
		{name: "span-level", in: Skip{TraceID: "t", SpanID: "s", Reason: "r"}, want: "trace t span s: r"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func readSpans(t *testing.T, file string, lines []string) []trace.Span {
	t.Helper()
	var in string
	switch {
	case file != "" && lines != nil:
		t.Fatal("test case sets both file and lines")
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		in = string(b)
	default:
		in = strings.Join(lines, "\n")
	}
	spans, err := trace.Read(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	return spans
}

func userInput(text string) evalcase.Input {
	return evalcase.Input{Messages: []evalcase.Message{{Role: "user", Content: text}}}
}

func source(traceID, spanID string) evalcase.Source {
	return evalcase.Source{Format: SourceFormat, TraceID: traceID, SpanID: spanID}
}

// draft is the Expected an import produces before human review.
func draft(calls ...evalcase.ExpectedCall) evalcase.Expected {
	if calls == nil {
		calls = []evalcase.ExpectedCall{}
	}
	return evalcase.Expected{
		LabelOrigin: evalcase.LabelOriginAutoDraft,
		Reviewed:    false,
		Trajectory:  &evalcase.Trajectory{Mode: evalcase.TrajectoryStrict, Calls: calls},
	}
}

func dump(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(b)
}
