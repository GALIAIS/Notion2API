package app

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// toolEvidence is one assistant call paired with its returned result, if any.
type toolEvidence struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result,omitempty"`
	Failed    bool   `json:"failed,omitempty"`
}

// agentLedger summarizes the tool history of a conversation so the model is told
// what has already been done instead of repeating itself.
type agentLedger struct {
	Completed           []toolEvidence `json:"completed,omitempty"`
	Pending             []toolEvidence `json:"pending,omitempty"`
	ToolRounds          int            `json:"tool_rounds"`
	RepeatedCall        bool           `json:"repeated_call,omitempty"`
	RepeatedFailure     bool           `json:"repeated_failure,omitempty"`
	RepetitionSignature string         `json:"repetition_signature,omitempty"`
}

var (
	toolFailureSignalPattern = regexp.MustCompile(`(?i)(exit\s*(code|status)?\s*[:=]?\s*[1-9]\d*|\berror\b|\bfailed\b|\bfailure\b|exception|traceback|timed?\s*out|permission denied|not found|refused)`)
	toolDigitPattern         = regexp.MustCompile(`\d+`)
)

// compactToolResult keeps the head and tail of a long tool result so the prompt
// stays bounded without discarding the parts that usually carry the verdict.
func compactToolResult(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit < 200 {
		limit = 200
	}
	if len(text) <= limit {
		return text
	}
	head := limit / 3
	tail := limit - head - 80
	if tail < 80 {
		tail = 80
	}
	if head+tail >= len(text) {
		return text
	}
	return text[:head] + fmt.Sprintf("\n... [truncated %d bytes] ...\n", len(text)-head-tail) + text[len(text)-tail:]
}

func buildAgentLedger(messages []toolTranscriptMessage, resultLimit int) agentLedger {
	calls := map[string]toolEvidence{}
	order := make([]string, 0, len(messages))
	for _, message := range messages {
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "assistant":
			for _, raw := range message.ToolCalls {
				entry := mapValue(raw)
				if entry == nil {
					continue
				}
				id := strings.TrimSpace(stringValue(entry["id"]))
				if id == "" {
					continue
				}
				function := mapValue(entry["function"])
				calls[id] = toolEvidence{
					ID:        id,
					Name:      strings.TrimSpace(stringValue(function["name"])),
					Arguments: canonicalToolArgumentsJSON(stringValue(function["arguments"])),
				}
				order = append(order, id)
			}
		case "tool":
			id := strings.TrimSpace(message.ToolCallID)
			evidence, ok := calls[id]
			if !ok {
				continue
			}
			evidence.Result = compactToolResult(message.Text, resultLimit)
			evidence.Failed = toolFailureSignalPattern.MatchString(evidence.Result)
			calls[id] = evidence
		}
	}

	ledger := agentLedger{}
	seenCall := map[string]int{}
	seenFailure := map[string]int{}
	for _, id := range order {
		evidence := calls[id]
		ledger.ToolRounds++
		signature := evidence.Name + "\x00" + evidence.Arguments
		seenCall[signature]++
		if seenCall[signature] >= 2 {
			ledger.RepeatedCall = true
			ledger.RepetitionSignature = signature
		}
		if evidence.Result == "" {
			ledger.Pending = append(ledger.Pending, evidence)
			continue
		}
		ledger.Completed = append(ledger.Completed, evidence)
		if evidence.Failed {
			failureSignature := signature + "\x00" + normalizeToolFailure(evidence.Result)
			seenFailure[failureSignature]++
			if seenFailure[failureSignature] >= 2 {
				ledger.RepeatedFailure = true
				ledger.RepetitionSignature = failureSignature
			}
		}
	}
	return ledger
}

func normalizeToolFailure(text string) string {
	lowered := strings.ToLower(text)
	lowered = toolDigitPattern.ReplaceAllString(lowered, "#")
	if len(lowered) > 500 {
		lowered = lowered[:500]
	}
	return lowered
}

// RouterContext renders the ledger as compact evidence for the router prompt.
func (l agentLedger) RouterContext() string {
	if l.ToolRounds == 0 {
		return ""
	}
	payload := struct {
		Completed    []toolEvidence `json:"completed,omitempty"`
		Pending      []toolEvidence `json:"pending,omitempty"`
		RepeatedCall bool           `json:"repeated_call,omitempty"`
	}{l.Completed, l.Pending, l.RepeatedCall}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	hint := "Use only this compact evidence. A completed call is final evidence; do not issue the same name and arguments again."
	if l.RepeatedFailure {
		hint += " The same call failed repeatedly; change strategy instead of retrying it unchanged."
	}
	return hint + "\nEVIDENCE_LEDGER: " + string(encoded)
}

func (l agentLedger) hasCompleted(name string, arguments string) bool {
	want := canonicalToolArgumentsJSON(arguments)
	for _, evidence := range l.Completed {
		if evidence.Name == name && canonicalToolArgumentsJSON(evidence.Arguments) == want {
			return true
		}
	}
	return false
}

// filterCompletedCalls drops calls whose exact name and arguments already have a
// returned result, which is the usual shape of a model loop.
func filterCompletedCalls(calls []DetectedToolCall, ledger agentLedger) []DetectedToolCall {
	if len(calls) == 0 || len(ledger.Completed) == 0 {
		return calls
	}
	out := make([]DetectedToolCall, 0, len(calls))
	for _, call := range calls {
		if ledger.hasCompleted(call.Name, string(call.Arguments)) {
			continue
		}
		out = append(out, call)
	}
	return out
}

// CanContinue reports whether another tool turn is allowed.
func (l agentLedger) CanContinue(maxRounds int) error {
	if maxRounds <= 0 {
		maxRounds = 16
	}
	if l.ToolRounds >= maxRounds {
		return fmt.Errorf("tool round limit reached: %d", maxRounds)
	}
	if len(l.Pending) > 0 {
		return fmt.Errorf("pending tool results must be returned before another turn")
	}
	return nil
}

func (l agentLedger) completedCallIDs() []string {
	out := make([]string, 0, len(l.Completed))
	for _, evidence := range l.Completed {
		out = append(out, evidence.ID)
	}
	sort.Strings(out)
	return out
}

// callScope makes generated call IDs unique per conversation position so a
// retried turn never reuses an ID the client has already resolved.
func (l agentLedger) callScope(messageCount int, suffix string) string {
	scope := fmt.Sprintf("%d:%v", messageCount, l.completedCallIDs())
	if suffix != "" {
		scope += ":" + suffix
	}
	return scope
}

func rescopeToolCallIDs(calls []DetectedToolCall, scope string) []DetectedToolCall {
	for i := range calls {
		calls[i].ID = scopedCallID(calls[i].Name, string(calls[i].Arguments), i, scope)
	}
	return calls
}

func (l agentLedger) summaryPayload() map[string]any {
	return map[string]any{
		"tool_rounds":      l.ToolRounds,
		"completed":        len(l.Completed),
		"pending":          len(l.Pending),
		"repeated_call":    l.RepeatedCall,
		"repeated_failure": l.RepeatedFailure,
	}
}
