// Package trace reads OpenTelemetry spans from JSONL files.
//
// Two line formats are accepted and normalised into Span:
//
//  1. Flat: one span per line, e.g. the shape shown in AgentCore / Strands docs
//     ({"traceId":..., "spanId":..., "name":..., "attributes":{...}, "events":[...]}).
//  2. OTLP JSON: one ExportTraceServiceRequest per line
//     ({"resourceSpans":[{"scopeSpans":[{"spans":[...]}]}]}), as written by the
//     OpenTelemetry Collector file exporter. Attributes in KeyValue-list form
//     are flattened into a map.
package trace

import "encoding/json"

// Status values of a span.
const (
	StatusUnset = "UNSET"
	StatusOK    = "OK"
	StatusError = "ERROR"
)

// Span is a normalised OpenTelemetry span.
type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Scope        string
	Start        int64 // unix nanoseconds; 0 if unknown
	End          int64 // unix nanoseconds; 0 if unknown
	Status       string
	StatusMsg    string
	Attributes   Attrs
	Events       []Event

	// Seq is the 0-based position of the span in the input. It is used to keep
	// ordering stable when timestamps are equal or missing.
	Seq int
}

// Event is a span event.
type Event struct {
	Name       string
	Time       int64
	Attributes Attrs
}

// Attrs holds span or event attributes. Numbers are json.Number (or int64/float64
// when they come from OTLP typed values) so that large integers keep precision.
type Attrs map[string]any

// String returns the attribute as a string, or "" if absent or not a string.
func (a Attrs) String(key string) string {
	s, _ := a[key].(string)
	return s
}

// Int returns the attribute as an integer.
func (a Attrs) Int(key string) (int64, bool) {
	switch v := a[key].(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, true
		}
		if f, err := v.Float64(); err == nil {
			return int64(f), true
		}
	case int64:
		return v, true
	case float64:
		return int64(v), true
	}
	return 0, false
}
