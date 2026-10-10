package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Read parses a JSONL stream of spans. Blank lines are ignored. Any malformed
// line aborts with an error that includes the 1-based line number.
func Read(r io.Reader) ([]Span, error) {
	// ReadBytes has no line-length limit, unlike bufio.Scanner. Traces that
	// capture prompts can easily exceed the scanner's 64KiB default.
	br := bufio.NewReaderSize(r, 1<<20)

	var spans []Span
	for lineNo := 1; ; lineNo++ {
		line, readErr := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			got, err := parseLine(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			for _, s := range got {
				s.Seq = len(spans)
				spans = append(spans, s)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return spans, nil
		}
		if readErr != nil {
			return nil, readErr
		}
	}
}

func parseLine(line []byte) ([]Span, error) {
	var wl wireLine
	if err := json.Unmarshal(line, &wl); err != nil {
		return nil, err
	}

	var out []Span
	if len(wl.ResourceSpans) > 0 {
		for _, rs := range wl.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, ws := range ss.Spans {
					out = append(out, ws.toSpan(ss.Scope.Name))
				}
			}
		}
	} else {
		out = append(out, wl.wireSpan.toSpan(""))
	}

	for _, s := range out {
		if s.TraceID == "" || s.SpanID == "" {
			return nil, errors.New("span is missing traceId or spanId")
		}
	}
	return out, nil
}
