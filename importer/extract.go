package importer

import (
	"encoding/json"
	"strings"

	"github.com/matsudan/kase/evalcase"
	"github.com/matsudan/kase/trace"
)

// Attribute and event names used by Strands and the OpenTelemetry GenAI
// semantic conventions. Strands emits two generations of conventions:
//
//   - legacy: span events gen_ai.user.message / gen_ai.choice / gen_ai.tool.message
//   - latest: attributes gen_ai.input.messages / gen_ai.output.messages and
//     gen_ai.tool.call.arguments / gen_ai.tool.call.result
//
// In the latest convention the message attributes are, by default, attributes
// of a gen_ai.client.inference.operation.details event; with the
// gen_ai_span_attributes_only opt-in they are span attributes. Both are read.
//
// Both conventions are supported; the latest wins when both are present.
const (
	attrOperation   = "gen_ai.operation.name"
	attrAgentName   = "gen_ai.agent.name"
	attrModel       = "gen_ai.request.model"
	attrToolName    = "gen_ai.tool.name"
	attrToolCallID  = "gen_ai.tool.call.id"
	attrToolArgs    = "gen_ai.tool.call.arguments"
	attrToolResult  = "gen_ai.tool.call.result"
	attrInputMsgs   = "gen_ai.input.messages"
	attrOutputMsgs  = "gen_ai.output.messages"
	attrErrorType   = "error.type"
	eventToolMsg    = "gen_ai.tool.message"
	eventChoice     = "gen_ai.choice"
	opInvokeAgent   = "invoke_agent"
	opExecuteTool   = "execute_tool"
	maxAncestorWalk = 1024
)

func operation(s trace.Span) string { return s.Attributes.String(attrOperation) }

// isInvocation reports whether s is an agent-level run: invoke_agent, or a
// Strands multi-agent run (invoke_swarm, invoke_graph).
func isInvocation(s trace.Span) bool { return strings.HasPrefix(operation(s), "invoke_") }

// genaiValue returns a GenAI attribute from the span or, failing that, from
// the first of its events that carries it.
func genaiValue(s trace.Span, key string) (any, bool) {
	if v, ok := s.Attributes[key]; ok {
		return v, true
	}
	for _, ev := range s.Events {
		if v, ok := ev.Attributes[key]; ok {
			return v, true
		}
	}
	return nil, false
}

// parseMaybeJSON decodes strings that contain a JSON object or array and
// returns every other value unchanged. Numbers keep precision (json.Number).
func parseMaybeJSON(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	t := strings.TrimSpace(s)
	if t == "" || (t[0] != '{' && t[0] != '[') {
		return v
	}
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil || dec.More() {
		return v
	}
	return out
}

// extractText pulls human text out of an attribute value that may hold
// serialised Strands content blocks, e.g. the legacy content attribute
// `[{"text": "..."}]`. A string is decoded only when it is such a block list;
// any other string, including one that happens to be valid JSON (structured
// output, a pasted payload), is returned verbatim.
func extractText(v any) string {
	if s, ok := v.(string); ok {
		if blocks, ok := contentBlocks(s); ok {
			// A list holding only tool use/results has no text.
			return structuredText(blocks)
		}
		return s
	}
	return structuredText(v)
}

// structuredText pulls human text out of already-decoded content: Strands
// blocks ([{"text": "..."}]) and GenAI "parts" ([{"type":"text","content":"..."}]).
// Strings are literal text and are never decoded. Non-text parts such as tool
// calls and reasoning are ignored.
func structuredText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, e := range x {
			if t := structuredText(e); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if typ, ok := x["type"].(string); ok && typ != "text" {
			return ""
		}
		if t, ok := x["text"].(string); ok {
			return t
		}
		for _, k := range []string{"content", "message", "parts"} {
			if val, ok := x[k]; ok {
				if t := structuredText(val); t != "" {
					return t
				}
			}
		}
	}
	return ""
}

// contentBlockKeys are the members of the Strands (Bedrock Converse)
// ContentBlock union.
var contentBlockKeys = map[string]bool{
	"text":             true,
	"toolUse":          true,
	"toolResult":       true,
	"image":            true,
	"document":         true,
	"video":            true,
	"reasoningContent": true,
	"citationsContent": true,
	"guardContent":     true,
	"cachePoint":       true,
}

// contentBlocks decodes s if it is a JSON-serialised, non-empty list of
// Strands content blocks: objects whose keys are all ContentBlock members.
// The check is strict so that ordinary JSON text is not mistaken for blocks;
// an unknown block type therefore surfaces as raw JSON instead of vanishing.
func contentBlocks(s string) ([]any, bool) {
	list, ok := parseMaybeJSON(s).([]any)
	if !ok || len(list) == 0 {
		return nil, false
	}
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok || len(m) == 0 {
			return nil, false
		}
		for k := range m {
			if !contentBlockKeys[k] {
				return nil, false
			}
		}
	}
	return list, true
}

type message struct {
	Role         string
	Text         string
	FinishReason string
}

// parseMessages reads gen_ai.input.messages / gen_ai.output.messages.
func parseMessages(v any) []message {
	list, ok := parseMaybeJSON(v).([]any)
	if !ok {
		return nil
	}
	var out []message
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		var text string
		if parts, ok := m["parts"]; ok {
			// GenAI parts are structured; their content strings are literal.
			text = structuredText(parts)
		} else {
			text = extractText(m["content"])
		}
		if text == "" {
			continue
		}
		role, _ := m["role"].(string)
		finish, _ := m["finish_reason"].(string)
		out = append(out, message{Role: role, Text: text, FinishReason: finish})
	}
	return out
}

// extractInput returns the conversation sent to the agent. The system prompt
// is agent configuration rather than case input, so it is not included.
func extractInput(s trace.Span) []evalcase.Message {
	if v, ok := genaiValue(s, attrInputMsgs); ok {
		var out []evalcase.Message
		for _, m := range parseMessages(v) {
			out = append(out, evalcase.Message{Role: m.Role, Content: m.Text})
		}
		if len(out) > 0 {
			return out
		}
	}

	var out []evalcase.Message
	for _, ev := range s.Events {
		var role string
		switch ev.Name {
		case "gen_ai.user.message":
			role = "user"
		case "gen_ai.assistant.message":
			role = "assistant"
		default:
			continue
		}
		if text := extractText(ev.Attributes["content"]); text != "" {
			out = append(out, evalcase.Message{Role: role, Content: text})
		}
	}
	return out
}

// extractOutput returns the agent's final answer and why it stopped.
func extractOutput(s trace.Span) (text, finish string) {
	if v, ok := genaiValue(s, attrOutputMsgs); ok {
		msgs := parseMessages(v)
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "" || msgs[i].Role == "assistant" {
				return msgs[i].Text, msgs[i].FinishReason
			}
		}
	}
	for i := len(s.Events) - 1; i >= 0; i-- {
		ev := s.Events[i]
		if ev.Name != eventChoice {
			continue
		}
		return extractText(ev.Attributes["message"]), ev.Attributes.String("finish_reason")
	}
	return "", ""
}

// extractToolCall converts an execute_tool span into a ToolCall.
func extractToolCall(s trace.Span, agent string) evalcase.ToolCall {
	tc := evalcase.ToolCall{
		ID:    s.Attributes.String(attrToolCallID),
		Name:  s.Attributes.String(attrToolName),
		Agent: agent,
		Error: s.Status == trace.StatusError || s.Attributes[attrErrorType] != nil,
	}
	if s.Start > 0 && s.End >= s.Start {
		tc.DurationMs = (s.End - s.Start) / 1_000_000
	}

	if v, ok := s.Attributes[attrToolArgs]; ok {
		tc.Args = parseMaybeJSON(v)
	}
	if v, ok := s.Attributes[attrToolResult]; ok {
		tc.Result = parseMaybeJSON(v)
	} else if v, ok := genaiValue(s, attrOutputMsgs); ok {
		// The latest convention omits gen_ai.tool.call.result for failed tools;
		// the response is still in the tool_call_response output part.
		tc.Result = toolCallResponse(v)
	}

	// Legacy convention: arguments in gen_ai.tool.message, result in gen_ai.choice.
	for _, ev := range s.Events {
		switch ev.Name {
		case eventToolMsg:
			if tc.Args == nil {
				if v, ok := ev.Attributes["content"]; ok {
					tc.Args = parseMaybeJSON(v)
				}
			}
			if tc.ID == "" {
				tc.ID = ev.Attributes.String("id")
			}
		case eventChoice:
			if tc.Result == nil {
				if v, ok := ev.Attributes["message"]; ok {
					tc.Result = parseMaybeJSON(v)
				}
			}
		}
	}
	return tc
}

// toolCallResponse returns the response of the first tool_call_response part
// in gen_ai.output.messages.
func toolCallResponse(v any) any {
	msgs, _ := parseMaybeJSON(v).([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		parts, _ := mm["parts"].([]any)
		for _, p := range parts {
			if pm, ok := p.(map[string]any); ok && pm["type"] == "tool_call_response" {
				return pm["response"]
			}
		}
	}
	return nil
}
