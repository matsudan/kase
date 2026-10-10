// Package evalcase defines the evaluation case format produced from traces.
//
// A case separates what was observed in a trace (Observed) from what the
// evaluation should require (Expected). Importing a trace only produces an
// auto-drafted Expected that must be reviewed by a human before it is trusted
// as ground truth.
package evalcase

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

// SchemaVersion is the version of the case format.
const SchemaVersion = 1

// Label origins for Expected.
const LabelOriginAutoDraft = "auto-draft"

type Case struct {
	ID            string   `json:"id"`
	SchemaVersion int      `json:"schema_version"`
	Input         Input    `json:"input"`
	Observed      Observed `json:"observed"`
	Expected      Expected `json:"expected"`
	Meta          Meta     `json:"meta"`
}

type Input struct {
	Messages []Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Observed is what actually happened in the traced run.
type Observed struct {
	// ToolCalls are in start-time order, except that the calls of a
	// concurrency group (see ToolCall.Group) are in canonical order, because
	// their relative order is not meaningful. See SortGroups.
	ToolCalls    []ToolCall `json:"tool_calls"`
	FinalOutput  string     `json:"final_output,omitempty"`
	FinishReason string     `json:"finish_reason,omitempty"`
}

type ToolCall struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Agent string `json:"agent,omitempty"`
	Args  any    `json:"args,omitempty"`
	// Group is non-zero for calls that were issued together (e.g. several
	// tool uses in one model turn, run concurrently). Adjacent calls with the
	// same Group may happen in any order relative to each other. Zero means
	// the call is ordered on its own.
	Group      int   `json:"group,omitempty"`
	Result     any   `json:"result,omitempty"`
	Error      bool  `json:"error,omitempty"`
	DurationMs int64 `json:"duration_ms,omitempty"`
}

// Expected is the ground truth. When LabelOrigin is auto-draft and Reviewed is
// false it is only a starting point copied from the observed run.
type Expected struct {
	LabelOrigin string      `json:"label_origin"`
	Reviewed    bool        `json:"reviewed"`
	Trajectory  *Trajectory `json:"trajectory,omitempty"`
}

// TrajectoryStrict requires the calls to match one to one and in order,
// except within a group (see ExpectedCall.Group), where any order matches.
const TrajectoryStrict = "strict"

type Trajectory struct {
	Mode  string         `json:"mode"`
	Calls []ExpectedCall `json:"calls"`
}

type ExpectedCall struct {
	Tool string `json:"tool"`
	Args any    `json:"args,omitempty"`
	// Group has the same meaning as ToolCall.Group.
	Group int `json:"group,omitempty"`
}

type Meta struct {
	Source     Source `json:"source"`
	Agent      string `json:"agent,omitempty"`
	Model      string `json:"model,omitempty"`
	StartedAt  string `json:"started_at,omitempty"` // RFC 3339, UTC
	DurationMs int64  `json:"duration_ms,omitempty"`
	Outcome    string `json:"outcome,omitempty"` // "ok" or "error"
	Usage      *Usage `json:"usage,omitempty"`
}

type Source struct {
	Format  string `json:"format"`
	TraceID string `json:"trace_id"`
	SpanID  string `json:"span_id"`
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
	TotalTokens  int64 `json:"total_tokens,omitempty"`
}

// ComputeID returns a content-addressed ID derived from the input messages and
// the observed trajectory: for each tool call its name, agent, arguments and
// concurrency group. Results, outputs, timestamps and labels are excluded so
// the ID stays stable when those change.
//
// The ID does not depend on how the case is represented: arguments are
// hashed in canonical form (so 1, 1.0 and 1e0 are equal, and a float64 equals
// the json.Number it was decoded from), and calls within a concurrency group
// are hashed in canonical order.
func (c *Case) ComputeID() (string, error) {
	calls := append([]ToolCall(nil), c.Observed.ToolCalls...)
	keys, err := callKeys(calls)
	if err != nil {
		return "", fmt.Errorf("hash case: %w", err)
	}
	sortGroups(calls, keys)

	payload := struct {
		Messages []Message         `json:"messages"`
		Calls    []json.RawMessage `json:"calls"`
	}{Messages: c.Input.Messages, Calls: keys}
	if payload.Calls == nil {
		payload.Calls = []json.RawMessage{}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("hash case: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:16]), nil
}

// SortGroups puts the calls of each concurrency group (adjacent calls sharing
// a non-zero Group) in canonical order, so that the order in which concurrent
// calls happened to start does not leak into the case. Other calls keep their
// positions.
func SortGroups(calls []ToolCall) error {
	keys, err := callKeys(calls)
	if err != nil {
		return err
	}
	sortGroups(calls, keys)
	return nil
}

// callKeys returns, for each call, the canonical JSON of the fields that
// identify it.
func callKeys(calls []ToolCall) ([]json.RawMessage, error) {
	type idCall struct {
		Tool  string          `json:"tool"`
		Agent string          `json:"agent,omitempty"`
		Args  json.RawMessage `json:"args,omitempty"`
		Group int             `json:"group,omitempty"`
	}
	var keys []json.RawMessage
	for _, tc := range calls {
		e := idCall{Tool: tc.Name, Agent: tc.Agent, Group: tc.Group}
		if tc.Args != nil {
			a, err := canonicalJSON(tc.Args)
			if err != nil {
				return nil, fmt.Errorf("tool call %q args: %w", tc.Name, err)
			}
			e.Args = a
		}
		k, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// sortGroups sorts calls and keys together by key within each group.
func sortGroups(calls []ToolCall, keys []json.RawMessage) {
	for i := 0; i < len(calls); {
		j := i + 1
		for calls[i].Group != 0 && j < len(calls) && calls[j].Group == calls[i].Group {
			j++
		}
		if j-i > 1 {
			sort.Stable(byKey{calls[i:j], keys[i:j]})
		}
		i = j
	}
}

type byKey struct {
	calls []ToolCall
	keys  []json.RawMessage
}

func (b byKey) Len() int           { return len(b.calls) }
func (b byKey) Less(i, j int) bool { return bytes.Compare(b.keys[i], b.keys[j]) < 0 }
func (b byKey) Swap(i, j int) {
	b.calls[i], b.calls[j] = b.calls[j], b.calls[i]
	b.keys[i], b.keys[j] = b.keys[j], b.keys[i]
}

// ReadJSONL reads cases written by WriteJSONL. Numbers inside arguments and
// results are decoded as json.Number, so they keep full precision and
// ComputeID on a read case reproduces its ID.
func ReadJSONL(r io.Reader) ([]Case, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var cases []Case
	for {
		var c Case
		err := dec.Decode(&c)
		if errors.Is(err, io.EOF) {
			return cases, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read case %d: %w", len(cases)+1, err)
		}
		cases = append(cases, c)
	}
}

// WriteJSONL writes one case per line.
func WriteJSONL(w io.Writer, cases []Case) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // keep placeholders like <EMAIL_1> readable
	for i := range cases {
		if err := enc.Encode(&cases[i]); err != nil {
			return fmt.Errorf("write case %s: %w", cases[i].ID, err)
		}
	}
	return nil
}
