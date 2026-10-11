// Package importer converts OpenTelemetry GenAI spans (as emitted by Strands
// Agents) into evaluation cases.
//
// One case is built per outermost invocation span: an invoke_agent span, or a
// Strands multi-agent span (invoke_swarm, invoke_graph), that is not nested in
// another invocation. A trace can therefore yield several cases, e.g. when an
// application span wraps more than one agent call.
package importer

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/matsudan/kase/evalcase"
	"github.com/matsudan/kase/trace"
)

// SourceFormat is recorded in Meta.Source.Format.
const SourceFormat = "otel-genai"

// Skip describes an invocation (or a whole trace, when SpanID is empty) that
// could not be turned into a case.
type Skip struct {
	TraceID string
	SpanID  string
	Reason  string
}

// String renders a Skip for logs.
func (s Skip) String() string {
	if s.SpanID == "" {
		return fmt.Sprintf("trace %s: %s", s.TraceID, s.Reason)
	}
	return fmt.Sprintf("trace %s span %s: %s", s.TraceID, s.SpanID, s.Reason)
}

// Result is the outcome of FromSpans. Cases are ordered by trace (first
// appearance in the input), then by invocation start time. Case IDs are
// unique: when several invocations yield the same ID (same input and
// trajectory), the first is kept and the others are reported in Skipped.
type Result struct {
	Cases   []evalcase.Case
	Skipped []Skip
}

// FromSpans groups spans by trace and builds one case per outermost invocation.
func FromSpans(spans []trace.Span) Result {
	var order []string
	byTrace := make(map[string][]trace.Span)
	for _, s := range spans {
		if _, seen := byTrace[s.TraceID]; !seen {
			order = append(order, s.TraceID)
		}
		byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
	}

	var res Result
	for _, id := range order {
		fromTrace(id, byTrace[id], &res)
	}
	dedupe(&res)
	return res
}

// dedupe drops cases whose ID was already produced, reporting each as a Skip.
func dedupe(res *Result) {
	first := make(map[string]evalcase.Source, len(res.Cases))
	kept := res.Cases[:0]
	for _, c := range res.Cases {
		if src, dup := first[c.ID]; dup {
			res.Skipped = append(res.Skipped, Skip{
				TraceID: c.Meta.Source.TraceID,
				SpanID:  c.Meta.Source.SpanID,
				Reason:  fmt.Sprintf("duplicate of case %s (trace %s span %s)", c.ID, src.TraceID, src.SpanID),
			})
			continue
		}
		first[c.ID] = c.Meta.Source
		kept = append(kept, c)
	}
	res.Cases = kept
}

func fromTrace(traceID string, spans []trace.Span, res *Result) {
	byID := make(map[string]trace.Span, len(spans))
	for _, s := range spans {
		byID[s.SpanID] = s
	}

	var roots []trace.Span
	toolsByRoot := make(map[string][]trace.Span)
	for _, s := range spans {
		chain := invocationAncestors(s, byID)
		switch {
		case isInvocation(s) && len(chain) == 0:
			roots = append(roots, s)
		case operation(s) == opExecuteTool && len(chain) > 0:
			root := chain[len(chain)-1]
			toolsByRoot[root.SpanID] = append(toolsByRoot[root.SpanID], s)
		}
	}
	if len(roots) == 0 {
		res.Skipped = append(res.Skipped, Skip{TraceID: traceID, Reason: "no invoke_* span found"})
		return
	}

	sortSpans(roots)
	for _, root := range roots {
		c, err := buildCase(traceID, root, toolsByRoot[root.SpanID], byID)
		if err != nil {
			res.Skipped = append(res.Skipped, Skip{TraceID: traceID, SpanID: root.SpanID, Reason: err.Error()})
			continue
		}
		res.Cases = append(res.Cases, c)
	}
}

func buildCase(traceID string, root trace.Span, tools []trace.Span, byID map[string]trace.Span) (evalcase.Case, error) {
	input := extractInput(root)
	if len(input) == 0 {
		return evalcase.Case{}, errors.New("no input messages found on the " + operation(root) + " span " +
			"(is message content capture enabled, or was it redacted?)")
	}

	sortSpans(tools)
	groups := concurrencyGroups(tools, byID)
	calls := make([]evalcase.ToolCall, 0, len(tools))
	for i, t := range tools {
		agentName := ""
		for _, a := range invocationAncestors(t, byID) {
			if operation(a) == opInvokeAgent {
				agentName = a.Attributes.String(attrAgentName)
				break
			}
		}
		tc := extractToolCall(t, agentName)
		tc.Group = groups[i]
		calls = append(calls, tc)
	}
	if err := evalcase.SortGroups(calls); err != nil {
		return evalcase.Case{}, err
	}

	output, finish := extractOutput(root)

	c := evalcase.Case{
		SchemaVersion: evalcase.SchemaVersion,
		Input:         evalcase.Input{Messages: input},
		Observed: evalcase.Observed{
			ToolCalls:    calls,
			FinalOutput:  output,
			FinishReason: finish,
		},
		Expected: draftExpected(calls),
		Meta:     buildMeta(traceID, root),
	}
	id, err := c.ComputeID()
	if err != nil {
		return evalcase.Case{}, err
	}
	c.ID = id
	return c, nil
}

// draftExpected copies the observed trajectory as a starting point for review.
func draftExpected(calls []evalcase.ToolCall) evalcase.Expected {
	traj := &evalcase.Trajectory{Mode: evalcase.TrajectoryStrict, Calls: make([]evalcase.ExpectedCall, 0, len(calls))}
	for _, c := range calls {
		traj.Calls = append(traj.Calls, evalcase.ExpectedCall{Tool: c.Name, Args: c.Args, Group: c.Group})
	}
	return evalcase.Expected{
		LabelOrigin: evalcase.LabelOriginAutoDraft,
		Reviewed:    false,
		Trajectory:  traj,
	}
}

func buildMeta(traceID string, root trace.Span) evalcase.Meta {
	m := evalcase.Meta{
		Source: evalcase.Source{Format: SourceFormat, TraceID: traceID, SpanID: root.SpanID},
		Agent:  root.Attributes.String(attrAgentName),
		Model:  root.Attributes.String(attrModel),
	}
	if root.Start > 0 {
		m.StartedAt = time.Unix(0, root.Start).UTC().Format(time.RFC3339Nano)
		if root.End >= root.Start {
			m.DurationMs = (root.End - root.Start) / 1_000_000
		}
	}
	if root.Status == trace.StatusError {
		m.Outcome = "error"
	} else {
		m.Outcome = "ok"
	}

	in, hasIn := root.Attributes.Int("gen_ai.usage.input_tokens")
	out, hasOut := root.Attributes.Int("gen_ai.usage.output_tokens")
	total, hasTotal := root.Attributes.Int("gen_ai.usage.total_tokens")
	if hasIn || hasOut || hasTotal {
		m.Usage = &evalcase.Usage{InputTokens: in, OutputTokens: out, TotalTokens: total}
	}
	return m
}

// invocationAncestors returns the invocation spans enclosing s (excluding s
// itself), nearest first. The last element is the outermost invocation.
func invocationAncestors(s trace.Span, byID map[string]trace.Span) []trace.Span {
	var out []trace.Span
	cur := s
	for i := 0; i < maxAncestorWalk && cur.ParentSpanID != ""; i++ {
		parent, ok := byID[cur.ParentSpanID]
		if !ok {
			break
		}
		if isInvocation(parent) {
			out = append(out, parent)
		}
		cur = parent
	}
	return out
}

// concurrencyGroups assigns a group number to tool spans (sorted by start
// time) that were issued together, so that their nondeterministic start order
// does not end up in the case. Consecutive spans belong to the same batch when
//
//   - they share a parent that is not an invocation span: Strands parents
//     tool spans to the event-loop cycle span, so these are tool uses from a
//     single model turn, whose relative order the model did not depend on and
//     which the default ConcurrentToolExecutor runs in parallel; or
//   - their time intervals overlap, which catches concurrency in traces
//     without per-turn spans and across agents (e.g. parallel graph nodes).
//
// Batches of two or more spans get group numbers 1, 2, ...; every other span
// gets 0.
func concurrencyGroups(tools []trace.Span, byID map[string]trace.Span) []int {
	groups := make([]int, len(tools))
	next := 1
	closeBatch := func(start, end int) {
		if end-start < 2 {
			return
		}
		for k := start; k < end; k++ {
			groups[k] = next
		}
		next++
	}

	start := 0
	var batchEnd int64 // latest end time in the current batch
	for i, t := range tools {
		if i > 0 && !sameTurn(tools[i-1], t, byID) && (t.Start <= 0 || t.Start >= batchEnd) {
			closeBatch(start, i)
			start, batchEnd = i, 0
		}
		batchEnd = max(batchEnd, t.End)
	}
	closeBatch(start, len(tools))
	return groups
}

// sameTurn reports whether a and b are children of the same per-turn span.
func sameTurn(a, b trace.Span, byID map[string]trace.Span) bool {
	if a.ParentSpanID == "" || a.ParentSpanID != b.ParentSpanID {
		return false
	}
	parent, ok := byID[a.ParentSpanID]
	return ok && !isInvocation(parent)
}

// sortSpans orders spans by start time, then by input order.
func sortSpans(spans []trace.Span) {
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		return spans[i].Seq < spans[j].Seq
	})
}
