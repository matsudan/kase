package evalcase

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func sample() Case {
	return Case{
		Input: Input{Messages: []Message{{Role: "user", Content: "hi"}}},
		Observed: Observed{ToolCalls: []ToolCall{
			{Name: "get", Args: map[string]any{"b": 2, "a": 1}, Result: "r1"},
		}},
	}
}

// withCalls returns sample() with its tool calls replaced.
func withCalls(calls ...ToolCall) Case {
	c := sample()
	c.Observed.ToolCalls = calls
	return c
}

// with returns sample() modified by f.
func with(f func(c *Case)) Case {
	c := sample()
	f(&c)
	return c
}

func TestComputeID(t *testing.T) {
	argsOf := func(v any) Case { return withCalls(ToolCall{Name: "get", Args: v}) }

	tests := []struct {
		name     string
		a, b     Case
		wantSame bool
	}{
		// Fields that must not affect the ID.
		{name: "identical", a: sample(), b: sample(), wantSame: true},
		{
			name: "tool result, id, error and duration",
			a:    sample(),
			b: with(func(c *Case) {
				tc := &c.Observed.ToolCalls[0]
				tc.Result, tc.ID, tc.Error, tc.DurationMs = "different", "call_9", true, 42
			}),
			wantSame: true,
		},
		{name: "final output", a: sample(), b: with(func(c *Case) { c.Observed.FinalOutput = "x" }), wantSame: true},
		{name: "finish reason", a: sample(), b: with(func(c *Case) { c.Observed.FinishReason = "max_tokens" }), wantSame: true},
		{name: "meta", a: sample(), b: with(func(c *Case) { c.Meta.Model = "other"; c.Meta.Source.TraceID = "x" }), wantSame: true},
		{name: "expected", a: sample(), b: with(func(c *Case) { c.Expected.Reviewed = true }), wantSame: true},
		{name: "stale id", a: sample(), b: with(func(c *Case) { c.ID = "sha256:stale" }), wantSame: true},

		// Representation of numbers must not affect the ID.
		{name: "int vs json.Number", a: argsOf(map[string]any{"n": 1}), b: argsOf(map[string]any{"n": json.Number("1")}), wantSame: true},
		{name: "1 vs 1.0", a: argsOf(map[string]any{"n": json.Number("1")}), b: argsOf(map[string]any{"n": json.Number("1.0")}), wantSame: true},
		{name: "100 vs 1e2", a: argsOf(map[string]any{"n": json.Number("100")}), b: argsOf(map[string]any{"n": json.Number("1E+2")}), wantSame: true},
		{name: "float64 vs json.Number", a: argsOf(map[string]any{"n": 10.5}), b: argsOf(map[string]any{"n": json.Number("10.50")}), wantSame: true},
		{name: "-0 vs 0", a: argsOf(map[string]any{"n": json.Number("-0.0")}), b: argsOf(map[string]any{"n": 0}), wantSame: true},
		{
			name:     "big int spellings",
			a:        argsOf(map[string]any{"n": json.Number("12345678901234567890")}),
			b:        argsOf(map[string]any{"n": json.Number("1.2345678901234567890e19")}),
			wantSame: true,
		},
		{name: "nested numbers", a: argsOf([]any{map[string]any{"n": 2}}), b: argsOf([]any{map[string]any{"n": json.Number("2.00")}}), wantSame: true},

		// Order within a concurrency group must not affect the ID.
		{
			name:     "order within group",
			a:        withCalls(ToolCall{Name: "a", Group: 1}, ToolCall{Name: "b", Group: 1}),
			b:        withCalls(ToolCall{Name: "b", Group: 1}, ToolCall{Name: "a", Group: 1}),
			wantSame: true,
		},
		{
			name: "order within group with args",
			a: withCalls(ToolCall{Name: "s", Args: map[string]any{"q": "x"}, Group: 1},
				ToolCall{Name: "s", Args: map[string]any{"q": "y"}, Group: 1}),
			b: withCalls(ToolCall{Name: "s", Args: map[string]any{"q": "y"}, Group: 1},
				ToolCall{Name: "s", Args: map[string]any{"q": "x"}, Group: 1}),
			wantSame: true,
		},

		// Fields that must affect the ID.
		{name: "tool args", a: sample(), b: argsOf(map[string]any{"a": 2})},
		{name: "tool name", a: sample(), b: with(func(c *Case) { c.Observed.ToolCalls[0].Name = "put" })},
		{name: "tool agent", a: sample(), b: with(func(c *Case) { c.Observed.ToolCalls[0].Agent = "Other" })},
		{name: "extra tool call", a: sample(), b: with(func(c *Case) {
			c.Observed.ToolCalls = append(c.Observed.ToolCalls, ToolCall{Name: "get"})
		})},
		{name: "no tool calls", a: sample(), b: withCalls()},
		{name: "input content", a: sample(), b: with(func(c *Case) { c.Input.Messages[0].Content = "bye" })},
		{name: "input role", a: sample(), b: with(func(c *Case) { c.Input.Messages[0].Role = "assistant" })},
		{name: "number vs string", a: argsOf(map[string]any{"n": 1}), b: argsOf(map[string]any{"n": "1"})},
		{
			// float64 would round both to the same value.
			name: "big ints differing in the last digit",
			a:    argsOf(map[string]any{"n": json.Number("12345678901234567890")}),
			b:    argsOf(map[string]any{"n": json.Number("12345678901234567891")}),
		},
		{
			name: "sequential order",
			a:    withCalls(ToolCall{Name: "a"}, ToolCall{Name: "b"}),
			b:    withCalls(ToolCall{Name: "b"}, ToolCall{Name: "a"}),
		},
		{
			name: "grouped vs sequential",
			a:    withCalls(ToolCall{Name: "a", Group: 1}, ToolCall{Name: "b", Group: 1}),
			b:    withCalls(ToolCall{Name: "a"}, ToolCall{Name: "b"}),
		},
		{
			name: "order across groups",
			a:    withCalls(ToolCall{Name: "a", Group: 1}, ToolCall{Name: "b", Group: 2}),
			b:    withCalls(ToolCall{Name: "b", Group: 1}, ToolCall{Name: "a", Group: 2}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.a.Observed.ToolCalls
			beforeCopy := append([]ToolCall(nil), before...)

			ida, err := tt.a.ComputeID()
			if err != nil {
				t.Fatalf("a.ComputeID() error = %v", err)
			}
			idb, err := tt.b.ComputeID()
			if err != nil {
				t.Fatalf("b.ComputeID() error = %v", err)
			}
			for _, id := range []string{ida, idb} {
				if !strings.HasPrefix(id, "sha256:") || len(id) != len("sha256:")+32 {
					t.Errorf("ComputeID() = %q, want sha256:<32 hex chars>", id)
				}
			}
			if same := ida == idb; same != tt.wantSame {
				t.Errorf("a = %s, b = %s; same = %v, want %v", ida, idb, same, tt.wantSame)
			}
			if !reflect.DeepEqual(tt.a.Observed.ToolCalls, beforeCopy) {
				t.Errorf("ComputeID() reordered the receiver's tool calls: %+v", tt.a.Observed.ToolCalls)
			}
		})
	}
}

func TestComputeIDError(t *testing.T) {
	c := withCalls(ToolCall{Name: "bad", Args: map[string]any{"f": func() {}}})
	if _, err := c.ComputeID(); err == nil || !strings.Contains(err.Error(), `tool call "bad" args`) {
		t.Fatalf("ComputeID() error = %v, want an args encoding error", err)
	}
}

func TestSortGroups(t *testing.T) {
	type call struct {
		name  string
		group int
	}
	tests := []struct {
		name string
		in   []call
		want []call
	}{
		{name: "empty"},
		{
			name: "ungrouped calls keep their order",
			in:   []call{{"b", 0}, {"a", 0}},
			want: []call{{"b", 0}, {"a", 0}},
		},
		{
			name: "group is sorted",
			in:   []call{{"c", 1}, {"a", 1}, {"b", 1}},
			want: []call{{"a", 1}, {"b", 1}, {"c", 1}},
		},
		{
			name: "only calls inside each group move",
			in:   []call{{"z", 0}, {"b", 1}, {"a", 1}, {"y", 0}, {"d", 2}, {"c", 2}},
			want: []call{{"z", 0}, {"a", 1}, {"b", 1}, {"y", 0}, {"c", 2}, {"d", 2}},
		},
		{
			name: "adjacent groups are not merged",
			in:   []call{{"b", 1}, {"a", 2}},
			want: []call{{"b", 1}, {"a", 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []ToolCall
			for _, c := range tt.in {
				calls = append(calls, ToolCall{Name: c.name, Group: c.group})
			}
			if err := SortGroups(calls); err != nil {
				t.Fatalf("SortGroups() error = %v", err)
			}
			var got []call
			for _, c := range calls {
				got = append(got, call{c.Name, c.Group})
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SortGroups() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCanonicalNumber(t *testing.T) {
	tests := []struct {
		in   json.Number
		want json.Number
	}{
		{in: "0", want: "0"},
		{in: "-0", want: "0"},
		{in: "0.000", want: "0"},
		{in: "0e10", want: "0"},
		{in: "1", want: "1"},
		{in: "1.0", want: "1"},
		{in: "1e0", want: "1"},
		{in: "10", want: "1e1"},
		{in: "100", want: "1e2"},
		{in: "1E+2", want: "1e2"},
		{in: "10.50", want: "105e-1"},
		{in: "0.05", want: "5e-2"},
		{in: "-1.5e3", want: "-15e2"},
		{in: "12345678901234567890", want: "1234567890123456789e1"},
		{in: "1e99999999999999999999", want: "1e99999999999999999999"}, // exponent overflow: unchanged
	}
	for _, tt := range tests {
		t.Run(string(tt.in), func(t *testing.T) {
			if got := canonicalNumber(tt.in); got != tt.want {
				t.Errorf("canonicalNumber(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestWriteJSONL(t *testing.T) {
	escaped := sample()
	escaped.ID = "sha256:esc"
	escaped.Input.Messages[0].Content = "<EMAIL_1> & more"

	second := sample()
	second.ID = "sha256:second"

	tests := []struct {
		name         string
		in           []Case
		wantIDs      []string // one line per case, in order
		wantContains string   // substring expected verbatim in the output
	}{
		{name: "nil", in: nil},
		{name: "empty", in: []Case{}},
		{
			name:    "one case per line",
			in:      []Case{escaped, second},
			wantIDs: []string{"sha256:esc", "sha256:second"},
		},
		{
			name:         "html escaping disabled",
			in:           []Case{escaped},
			wantIDs:      []string{"sha256:esc"},
			wantContains: "<EMAIL_1> & more",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteJSONL(&buf, tt.in); err != nil {
				t.Fatalf("WriteJSONL() error = %v", err)
			}
			out := buf.String()

			var gotIDs []string
			for line := range strings.Lines(out) {
				var c Case
				if err := json.Unmarshal([]byte(line), &c); err != nil {
					t.Fatalf("line %q is not a JSON case: %v", line, err)
				}
				gotIDs = append(gotIDs, c.ID)
			}
			if strings.Join(gotIDs, ",") != strings.Join(tt.wantIDs, ",") {
				t.Errorf("ids = %q, want %q", gotIDs, tt.wantIDs)
			}
			if out != "" && !strings.HasSuffix(out, "\n") {
				t.Errorf("output must end with a newline: %q", out)
			}
			if !strings.Contains(out, tt.wantContains) {
				t.Errorf("output does not contain %q:\n%s", tt.wantContains, out)
			}
		})
	}
}

// Cases written by WriteJSONL and read back by ReadJSONL must keep their IDs.
func TestReadJSONLRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   Case
	}{
		{name: "sample", in: sample()},
		{name: "no tool calls", in: withCalls()},
		{
			// Lost by a plain json.Unmarshal into float64.
			name: "big integer args",
			in:   withCalls(ToolCall{Name: "get", Args: map[string]any{"id": json.Number("12345678901234567891")}}),
		},
		{name: "float args", in: withCalls(ToolCall{Name: "get", Args: map[string]any{"x": 0.1}})},
		{
			name: "concurrency group",
			in:   withCalls(ToolCall{Name: "a", Group: 1}, ToolCall{Name: "b", Group: 1}, ToolCall{Name: "c"}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.in
			id, err := c.ComputeID()
			if err != nil {
				t.Fatal(err)
			}
			c.ID = id

			var buf bytes.Buffer
			if err := WriteJSONL(&buf, []Case{c}); err != nil {
				t.Fatal(err)
			}
			got, err := ReadJSONL(&buf)
			if err != nil {
				t.Fatalf("ReadJSONL() error = %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("ReadJSONL() returned %d cases, want 1", len(got))
			}
			if got[0].ID != id {
				t.Errorf("ID = %q, want %q", got[0].ID, id)
			}
			recomputed, err := got[0].ComputeID()
			if err != nil {
				t.Fatal(err)
			}
			if recomputed != id {
				t.Errorf("ComputeID() after round trip = %q, want %q", recomputed, id)
			}
		})
	}
}

func TestReadJSONL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantIDs []string
		wantErr string
	}{
		{name: "empty", in: ""},
		{name: "blank lines", in: "\n\n", wantIDs: nil},
		{name: "two cases", in: `{"id":"a"}` + "\n" + `{"id":"b"}` + "\n", wantIDs: []string{"a", "b"}},
		{name: "no trailing newline", in: `{"id":"a"}`, wantIDs: []string{"a"}},
		{name: "malformed second case", in: `{"id":"a"}` + "\n{oops\n", wantErr: "read case 2:"},
		{name: "wrong type", in: `{"id":1}`, wantErr: "read case 1:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadJSONL(strings.NewReader(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ReadJSONL() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadJSONL() error = %v", err)
			}
			var ids []string
			for _, c := range got {
				ids = append(ids, c.ID)
			}
			if !reflect.DeepEqual(ids, tt.wantIDs) {
				t.Errorf("ids = %q, want %q", ids, tt.wantIDs)
			}
		})
	}
}
