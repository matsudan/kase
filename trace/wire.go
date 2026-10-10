package trace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// wireLine is one JSONL line: either a flat span or an OTLP export request.
type wireLine struct {
	wireSpan
	ResourceSpans []wireResourceSpans `json:"resourceSpans"`
}

type wireResourceSpans struct {
	ScopeSpans []struct {
		Scope struct {
			Name string `json:"name"`
		} `json:"scope"`
		Spans []wireSpan `json:"spans"`
	} `json:"scopeSpans"`
}

type wireSpan struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	ParentSpanID      string     `json:"parentSpanId"`
	Name              string     `json:"name"`
	StartTimeUnixNano nanoTime   `json:"startTimeUnixNano"`
	EndTimeUnixNano   nanoTime   `json:"endTimeUnixNano"`
	Status            wireStatus `json:"status"`
	Attributes        Attrs      `json:"attributes"`
	Events            []struct {
		Name         string   `json:"name"`
		TimeUnixNano nanoTime `json:"timeUnixNano"`
		Attributes   Attrs    `json:"attributes"`
	} `json:"events"`
	Scope struct {
		Name string `json:"name"`
	} `json:"scope"`
}

func (w wireSpan) toSpan(scope string) Span {
	if scope == "" {
		scope = w.Scope.Name
	}
	s := Span{
		TraceID:      w.TraceID,
		SpanID:       w.SpanID,
		ParentSpanID: w.ParentSpanID,
		Name:         w.Name,
		Scope:        scope,
		Start:        int64(w.StartTimeUnixNano),
		End:          int64(w.EndTimeUnixNano),
		Status:       w.Status.Code,
		StatusMsg:    w.Status.Message,
		Attributes:   w.Attributes,
	}
	if s.Status == "" {
		s.Status = StatusUnset
	}
	for _, e := range w.Events {
		s.Events = append(s.Events, Event{Name: e.Name, Time: int64(e.TimeUnixNano), Attributes: e.Attributes})
	}
	return s
}

// nanoTime accepts a JSON number or a numeric string (OTLP JSON uses strings).
type nanoTime int64

func (n *nanoTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return fmt.Errorf("invalid unix nano timestamp %q", s)
		}
		v = int64(f)
	}
	*n = nanoTime(v)
	return nil
}

// wireStatus accepts {"code": 2 | "STATUS_CODE_ERROR" | "ERROR", "message": ...}
// or a bare string.
type wireStatus struct {
	Code    string
	Message string
}

func (s *wireStatus) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s.Code = normaliseStatus(str)
		return nil
	}
	var raw struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	s.Code = normaliseStatus(string(raw.Code))
	s.Message = raw.Message
	return nil
}

func normaliseStatus(s string) string {
	s = strings.ToUpper(strings.Trim(strings.TrimSpace(s), `"`))
	switch {
	case s == "2" || strings.Contains(s, "ERROR"):
		return StatusError
	case s == "1" || s == "OK" || strings.HasSuffix(s, "_OK"):
		return StatusOK
	}
	return StatusUnset
}

// UnmarshalJSON accepts either a plain JSON object or an OTLP KeyValue list.
func (a *Attrs) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*a = nil
		return nil
	}
	switch b[0] {
	case '{':
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return err
		}
		*a = Attrs(m)
	case '[':
		var kvs []keyValue
		if err := json.Unmarshal(b, &kvs); err != nil {
			return err
		}
		m := make(Attrs, len(kvs))
		for _, kv := range kvs {
			m[kv.Key] = kv.Value.decode()
		}
		*a = m
	default:
		return fmt.Errorf("attributes must be an object or a key-value list")
	}
	return nil
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

// anyValue is the OTLP JSON AnyValue.
type anyValue struct {
	StringValue *string      `json:"stringValue"`
	IntValue    *json.Number `json:"intValue"`
	DoubleValue *float64     `json:"doubleValue"`
	BoolValue   *bool        `json:"boolValue"`
	BytesValue  *string      `json:"bytesValue"`
	ArrayValue  *struct {
		Values []anyValue `json:"values"`
	} `json:"arrayValue"`
	KvlistValue *struct {
		Values []keyValue `json:"values"`
	} `json:"kvlistValue"`
}

func (v anyValue) decode() any {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.IntValue != nil:
		return *v.IntValue
	case v.DoubleValue != nil:
		return *v.DoubleValue
	case v.BoolValue != nil:
		return *v.BoolValue
	case v.BytesValue != nil:
		return *v.BytesValue
	case v.ArrayValue != nil:
		out := make([]any, 0, len(v.ArrayValue.Values))
		for _, e := range v.ArrayValue.Values {
			out = append(out, e.decode())
		}
		return out
	case v.KvlistValue != nil:
		out := make(map[string]any, len(v.KvlistValue.Values))
		for _, kv := range v.KvlistValue.Values {
			out[kv.Key] = kv.Value.decode()
		}
		return out
	}
	return nil
}
