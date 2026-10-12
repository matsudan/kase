package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// spanSummary is the subset of Span checked by the fixture tests. Attributes
// are compared only for the integer keys listed in IntAttrs.
type spanSummary struct {
	SpanID       string
	ParentSpanID string
	Name         string
	Start, End   int64
	Status       string
	StatusMsg    string
	Seq          int
	Events       []string // event names
	IntAttrs     map[string]int64
}

func summarize(s Span, intKeys map[string]int64) spanSummary {
	sum := spanSummary{
		SpanID:       s.SpanID,
		ParentSpanID: s.ParentSpanID,
		Name:         s.Name,
		Start:        s.Start,
		End:          s.End,
		Status:       s.Status,
		StatusMsg:    s.StatusMsg,
		Seq:          s.Seq,
	}
	for _, e := range s.Events {
		sum.Events = append(sum.Events, e.Name)
	}
	for k := range intKeys {
		if sum.IntAttrs == nil {
			sum.IntAttrs = make(map[string]int64)
		}
		if n, ok := s.Attributes.Int(k); ok {
			sum.IntAttrs[k] = n
		}
	}
	return sum
}

func TestReadFixtures(t *testing.T) {
	tests := []struct {
		name string
		file string
		want []spanSummary
	}{
		{
			name: "flat (strands legacy)",
			file: "../testdata/strands_legacy.jsonl",
			want: []spanSummary{
				{
					SpanID: "0000000000000003", ParentSpanID: "0000000000000002",
					Name:  "execute_tool get_weather",
					Start: 1776929865000000000, End: 1776929865400000000,
					Status: StatusUnset, Seq: 0,
					Events: []string{"gen_ai.tool.message", "gen_ai.choice"},
				},
				{
					SpanID: "0000000000000004", ParentSpanID: "0000000000000002",
					Name:  "chat",
					Start: 1776929864100000000, End: 1776929864900000000,
					Status: StatusUnset, Seq: 1,
				},
				{
					SpanID: "0000000000000002", ParentSpanID: "0000000000000001",
					Name: "invoke_agent TravelAgent",
					// Must survive without float64 rounding.
					Start: 1776929864011990383, End: 1776929869634304466,
					Status: StatusUnset, Seq: 2,
					Events:   []string{"gen_ai.user.message", "gen_ai.choice"},
					IntAttrs: map[string]int64{"gen_ai.usage.total_tokens": 1215},
				},
			},
		},
		{
			name: "otlp json (latest convention)",
			file: "../testdata/otlp_latest.jsonl",
			want: []spanSummary{
				{
					SpanID: "00000000000000a3", ParentSpanID: "00000000000000a1",
					Name: "execute_tool refund",
					// Timestamps are numeric strings in OTLP JSON.
					Start: 1776929866000000000, End: 1776929866100000000,
					Status: StatusError, StatusMsg: "boom", Seq: 0,
				},
				{
					SpanID: "00000000000000a2", ParentSpanID: "00000000000000a1",
					Name:  "execute_tool get_order",
					Start: 1776929865000000000, End: 1776929865200000000,
					Status: StatusUnset, Seq: 1,
				},
				{
					SpanID: "00000000000000a1",
					Name:   "invoke_agent RefundAgent",
					Start:  1776929864000000000, End: 1776929868000000000,
					Status: StatusOK, Seq: 2,
					IntAttrs: map[string]int64{"gen_ai.usage.input_tokens": 120},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := os.Open(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			spans, err := Read(f)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if len(spans) != len(tt.want) {
				t.Fatalf("Read() returned %d spans, want %d", len(spans), len(tt.want))
			}
			for i, s := range spans {
				if got := summarize(s, tt.want[i].IntAttrs); !reflect.DeepEqual(got, tt.want[i]) {
					t.Errorf("span[%d]:\n got %+v\nwant %+v", i, got, tt.want[i])
				}
			}
		})
	}
}

func TestRead(t *testing.T) {
	long := strings.Repeat("x", 3<<20) // larger than bufio.Scanner's 64KiB default

	tests := []struct {
		name    string
		in      string
		want    []Span
		wantErr string // substring of the error; empty means no error
	}{
		{name: "empty input", in: ""},
		{name: "only blank lines", in: "\n  \n\t\n"},
		{
			name: "minimal flat span",
			in:   `{"traceId":"a","spanId":"b"}` + "\n",
			want: []Span{{TraceID: "a", SpanID: "b", Status: StatusUnset}},
		},
		{
			name: "no trailing newline",
			in:   `{"traceId":"a","spanId":"b"}`,
			want: []Span{{TraceID: "a", SpanID: "b", Status: StatusUnset}},
		},
		{
			name: "crlf line endings",
			in:   "{\"traceId\":\"a\",\"spanId\":\"b\"}\r\n{\"traceId\":\"a\",\"spanId\":\"c\"}\r\n",
			want: []Span{
				{TraceID: "a", SpanID: "b", Status: StatusUnset, Seq: 0},
				{TraceID: "a", SpanID: "c", Status: StatusUnset, Seq: 1},
			},
		},
		{
			name: "blank lines are skipped and do not consume seq",
			in:   "\n{\"traceId\":\"a\",\"spanId\":\"b\"}\n\n{\"traceId\":\"a\",\"spanId\":\"c\"}\n",
			want: []Span{
				{TraceID: "a", SpanID: "b", Status: StatusUnset, Seq: 0},
				{TraceID: "a", SpanID: "c", Status: StatusUnset, Seq: 1},
			},
		},
		{
			name: "line longer than scanner limit",
			in:   `{"traceId":"a","spanId":"b","attributes":{"k":"` + long + `"}}`,
			want: []Span{{TraceID: "a", SpanID: "b", Status: StatusUnset, Attributes: Attrs{"k": long}}},
		},
		{
			name: "flat attributes keep number precision",
			in:   `{"traceId":"a","spanId":"b","attributes":{"n":12345678901234567890}}`,
			want: []Span{{TraceID: "a", SpanID: "b", Status: StatusUnset,
				Attributes: Attrs{"n": json.Number("12345678901234567890")}}},
		},
		{
			name: "otlp spans across scopes get consecutive seq",
			in: `{"resourceSpans":[{"scopeSpans":[` +
				`{"scope":{"name":"s1"},"spans":[{"traceId":"t","spanId":"1"},{"traceId":"t","spanId":"2"}]},` +
				`{"scope":{"name":"s2"},"spans":[{"traceId":"t","spanId":"3"}]}]}]}`,
			want: []Span{
				{TraceID: "t", SpanID: "1", Status: StatusUnset, Seq: 0},
				{TraceID: "t", SpanID: "2", Status: StatusUnset, Seq: 1},
				{TraceID: "t", SpanID: "3", Status: StatusUnset, Seq: 2},
			},
		},
		{
			name: "otlp key-value attributes and events",
			in: `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"t","spanId":"1",` +
				`"attributes":[` +
				`{"key":"s","value":{"stringValue":"v"}},` +
				`{"key":"i","value":{"intValue":"7"}},` +
				`{"key":"d","value":{"doubleValue":1.5}},` +
				`{"key":"b","value":{"boolValue":true}},` +
				`{"key":"arr","value":{"arrayValue":{"values":[{"stringValue":"x"},{"intValue":"1"}]}}},` +
				`{"key":"kv","value":{"kvlistValue":{"values":[{"key":"k","value":{"stringValue":"v"}}]}}},` +
				`{"key":"empty","value":{}}],` +
				`"events":[{"name":"e","timeUnixNano":"5","attributes":[{"key":"k","value":{"stringValue":"v"}}]}]}]}]}]}`,
			want: []Span{{
				TraceID: "t", SpanID: "1", Status: StatusUnset,
				Attributes: Attrs{
					"s":     "v",
					"i":     json.Number("7"),
					"d":     1.5,
					"b":     true,
					"arr":   []any{"x", json.Number("1")},
					"kv":    map[string]any{"k": "v"},
					"empty": nil,
				},
				Events: []Event{{Name: "e", Time: 5, Attributes: Attrs{"k": "v"}}},
			}},
		},
		{
			name: "otlp request without spans",
			in:   `{"resourceSpans":[{"scopeSpans":[]}]}`,
		},
		{
			name:    "bad json reports line number",
			in:      "{\"traceId\":\"a\",\"spanId\":\"b\"}\n{oops\n",
			wantErr: "line 2:",
		},
		{
			name:    "line number counts blank lines",
			in:      "\n{\"traceId\":\"a\",\"spanId\":\"b\"}\n\n{oops\n",
			wantErr: "line 4:",
		},
		{
			name:    "missing ids",
			in:      `{"name":"x"}`,
			wantErr: "line 1: span is missing traceId or spanId",
		},
		{
			name:    "missing ids in otlp span",
			in:      `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"t"}]}]}]}`,
			wantErr: "line 1: span is missing traceId or spanId",
		},
		{
			name:    "attributes of wrong type",
			in:      `{"traceId":"a","spanId":"b","attributes":5}`,
			wantErr: "attributes must be an object or a key-value list",
		},
		{
			name:    "invalid timestamp",
			in:      `{"traceId":"a","spanId":"b","startTimeUnixNano":"soon"}`,
			wantErr: "invalid unix nano timestamp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Read(strings.NewReader(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Read() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Read() =\n %#v\nwant\n %#v", truncate(got), truncate(tt.want))
			}
		})
	}
}

// truncate keeps failure output readable when a span carries a huge attribute.
func truncate(spans []Span) string {
	const limit = 2000
	s := fmt.Sprintf("%#v", spans)
	if len(s) > limit {
		return s[:limit] + "...(truncated)"
	}
	return s
}

func TestNanoTimeUnmarshal(t *testing.T) {
	tests := []struct {
		in      string
		want    nanoTime
		wantErr bool
	}{
		{in: `1776929864011990383`, want: 1776929864011990383},
		{in: `"1776929864011990383"`, want: 1776929864011990383},
		{in: `""`, want: 0},
		{in: `null`, want: 0},
		{in: `1.5e3`, want: 1500},
		{in: `"abc"`, wantErr: true},
		{in: `true`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := nanoTime(-1)
			err := got.UnmarshalJSON([]byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("UnmarshalJSON(%s) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("UnmarshalJSON(%s) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestWireStatusUnmarshal(t *testing.T) {
	tests := []struct {
		in   string
		want wireStatus
	}{
		{in: `null`, want: wireStatus{}},
		{in: `{}`, want: wireStatus{Code: StatusUnset}},
		{in: `{"code":0}`, want: wireStatus{Code: StatusUnset}},
		{in: `{"code":1}`, want: wireStatus{Code: StatusOK}},
		{in: `{"code":2,"message":"boom"}`, want: wireStatus{Code: StatusError, Message: "boom"}},
		{in: `{"code":"STATUS_CODE_UNSET"}`, want: wireStatus{Code: StatusUnset}},
		{in: `{"code":"STATUS_CODE_OK"}`, want: wireStatus{Code: StatusOK}},
		{in: `{"code":"STATUS_CODE_ERROR"}`, want: wireStatus{Code: StatusError}},
		{in: `{"code":"error"}`, want: wireStatus{Code: StatusError}},
		{in: `"OK"`, want: wireStatus{Code: StatusOK}},
		{in: `"ERROR"`, want: wireStatus{Code: StatusError}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			var got wireStatus
			if err := got.UnmarshalJSON([]byte(tt.in)); err != nil {
				t.Fatalf("UnmarshalJSON(%s) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("UnmarshalJSON(%s) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestAttrsInt(t *testing.T) {
	attrs := Attrs{
		"number":     json.Number("42"),
		"number_flt": json.Number("42.9"),
		"int64":      int64(7),
		"float64":    float64(3.9),
		"string":     "42",
		"bool":       true,
	}
	tests := []struct {
		key    string
		want   int64
		wantOK bool
	}{
		{key: "number", want: 42, wantOK: true},
		{key: "number_flt", want: 42, wantOK: true},
		{key: "int64", want: 7, wantOK: true},
		{key: "float64", want: 3, wantOK: true},
		{key: "string"},
		{key: "bool"},
		{key: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, ok := attrs.Int(tt.key)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Int(%q) = %d, %v; want %d, %v", tt.key, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
