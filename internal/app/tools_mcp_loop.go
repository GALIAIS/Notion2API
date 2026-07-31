package app

import (
	"net/http"
	"strings"
)

// mcpToolLoopOutcome is the result of resolving one request through the
// server-side MCP loop. Exactly one of Calls / Result is meaningful: Calls means
// the caller must execute client-declared tools, Result means the answer is
// final.
type mcpToolLoopOutcome struct {
	Calls    []DetectedToolCall
	Result   InferenceResult
	HasCalls bool
	Rounds   int
}

// withMCPTools returns a copy of the turn whose catalog also contains the live
// MCP tools. Client-declared tools win on name collision, since the caller is
// the one that will execute them.
func (a *App) withMCPTools(turn toolTurnContext, enabled bool) toolTurnContext {
	if !enabled || a == nil || a.State == nil {
		return turn
	}
	mcpTools := a.State.MCP.Tools()
	if len(mcpTools) == 0 {
		return turn
	}
	existing := allowedToolNames(turn.Tools)
	merged := make([]ToolDefinition, 0, len(turn.Tools)+len(mcpTools))
	merged = append(merged, turn.Tools...)
	for _, tool := range mcpTools {
		if existing[tool.Name] {
			continue
		}
		merged = append(merged, tool)
	}
	turn.Tools = merged
	return turn
}

// splitToolCallsByOwner separates calls the gateway executes itself from calls
// that belong to the client.
func (a *App) splitToolCallsByOwner(calls []DetectedToolCall, turn toolTurnContext) (serverSide []DetectedToolCall, clientSide []DetectedToolCall) {
	for _, call := range calls {
		if tool, ok := findToolDefinition(call.Name, turn.Tools); ok && tool.Source == toolSourceMCP && a.State.MCP.Owns(call.Name) {
			serverSide = append(serverSide, call)
			continue
		}
		clientSide = append(clientSide, call)
	}
	return serverSide, clientSide
}

// executeMCPCalls runs each server-side call and records the outcome as ledger
// evidence. A tool that reports an error is recorded as failed evidence rather
// than aborting the turn, so the model can adapt.
func (a *App) executeMCPCalls(r *http.Request, calls []DetectedToolCall, turn *toolTurnContext) {
	assistantCall := toolTranscriptMessage{Role: "assistant"}
	for _, call := range calls {
		assistantCall.ToolCalls = append(assistantCall.ToolCalls, map[string]any{
			"id":       call.ID,
			"type":     "function",
			"function": map[string]any{"name": call.Name, "arguments": string(call.Arguments)},
		})
	}
	turn.Transcript = append(turn.Transcript, assistantCall)

	for _, call := range calls {
		var args map[string]any
		if decoded := decodeJSONObjectAny(string(call.Arguments)); decoded != nil {
			args = decoded
		} else {
			args = map[string]any{}
		}
		text, isError, err := a.State.MCP.CallTool(r.Context(), call.Name, args)
		if err != nil {
			text = "tool execution failed: " + err.Error()
			isError = true
		}
		if isError && !strings.Contains(strings.ToLower(text), "error") {
			text = "error: " + text
		}
		turn.Transcript = append(turn.Transcript, toolTranscriptMessage{
			Role:       "tool",
			ToolCallID: call.ID,
			Name:       call.Name,
			Text:       text,
		})
	}
	turn.Ledger = buildAgentLedger(turn.Transcript, turn.resultLimit())
}

// detachUpstreamThread prepares a request for another upstream round inside the
// tool loop. A continued round must not replay the previous thread scaffold,
// otherwise the same user turn is appended to the Notion thread twice.
func detachUpstreamThread(request PromptRunRequest) PromptRunRequest {
	request.UpstreamThreadID = ""
	request.SessionRepeatTurn = false
	request.ForceSessionRepeatTurn = false
	request.continuationDraft = nil
	request.continuationScaffold = nil
	return request
}

// resolveToolTurn drives the decide-execute cycle. MCP tools are executed
// in-process and fed back to the model; client-declared tools stop the loop and
// are returned so the HTTP layer can emit tool_calls.
func (a *App) resolveToolTurn(r *http.Request, request PromptRunRequest, turn toolTurnContext) (mcpToolLoopOutcome, PromptRunRequest, toolTurnContext, error) {
	return a.resolveToolTurnFrom(r, request, turn, 0)
}

// resolveToolTurnFrom is resolveToolTurn with an explicit starting round, so a
// turn resumed after an already-decided call keeps counting toward MaxRounds and
// native mode does not re-skip its first decision round.
func (a *App) resolveToolTurnFrom(r *http.Request, request PromptRunRequest, turn toolTurnContext, startRound int) (mcpToolLoopOutcome, PromptRunRequest, toolTurnContext, error) {
	maxRounds := turn.Settings.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 16
	}
	if startRound < 0 {
		startRound = 0
	}
	for round := startRound; round < maxRounds; round++ {
		calls, err := a.decideToolCalls(r, request, turn, round)
		if err != nil {
			return mcpToolLoopOutcome{}, request, turn, err
		}
		if len(calls) == 0 {
			return mcpToolLoopOutcome{Rounds: round}, request, turn, nil
		}
		serverSide, clientSide := a.splitToolCallsByOwner(calls, turn)
		if len(clientSide) > 0 {
			// A client tool ends the gateway's turn: only the caller can run it.
			return mcpToolLoopOutcome{Calls: clientSide, HasCalls: true, Rounds: round}, request, turn, nil
		}
		a.executeMCPCalls(r, serverSide, &turn)
		request = detachUpstreamThread(turn.applyToolTranscriptPrompt(request))
	}
	return mcpToolLoopOutcome{Rounds: maxRounds}, request, turn, nil
}

// continueToolTurnAfterCalls resumes the loop from calls that were already
// extracted from an answer round. Client-declared calls are handed straight
// back; server-side MCP calls are executed and the model is asked again, so the
// caller ends up with a finished text answer instead of a tool request.
func (a *App) continueToolTurnAfterCalls(r *http.Request, request PromptRunRequest, turn toolTurnContext, calls []DetectedToolCall) (mcpToolLoopOutcome, PromptRunRequest, toolTurnContext, error) {
	serverSide, clientSide := a.splitToolCallsByOwner(calls, turn)
	if len(clientSide) > 0 {
		return mcpToolLoopOutcome{Calls: clientSide, HasCalls: true}, request, turn, nil
	}
	if len(serverSide) == 0 {
		return mcpToolLoopOutcome{}, request, turn, nil
	}
	a.executeMCPCalls(r, serverSide, &turn)
	request = detachUpstreamThread(turn.applyToolTranscriptPrompt(request))

	outcome, request, turn, err := a.resolveToolTurnFrom(r, request, turn, 1)
	if err != nil {
		return mcpToolLoopOutcome{}, request, turn, err
	}
	if outcome.HasCalls {
		return outcome, request, turn, nil
	}
	// Every pending call now has evidence, so ask for the final answer.
	answer, err := a.runPrompt(r, request)
	if err != nil {
		return mcpToolLoopOutcome{}, request, turn, err
	}
	answer = applyInferenceResultOutputPolicy(answer, request)
	answer.Text = stripToolCallMarkup(answer.Text, turn.Tools)
	outcome.Result = answer
	return outcome, request, turn, nil
}

// decideToolCalls performs one decision round in the configured planning mode.
func (a *App) decideToolCalls(r *http.Request, request PromptRunRequest, turn toolTurnContext, round int) ([]DetectedToolCall, error) {
	if turn.PlanningMode == toolPlanningModeRouter {
		return a.runToolRouterRound(r, request, turn)
	}
	// Native mode asks the answering model directly; the call is extracted from
	// its text. Only used inside the MCP loop for rounds beyond the first, since
	// the first native round is handled by the normal answer path.
	if round == 0 {
		return nil, nil
	}
	native := turn.applyNativeToolPrompt(request)
	result, err := a.runPrompt(r, native)
	if err != nil {
		return nil, err
	}
	return turn.finalizeToolCalls(extractToolCallsFromText(result.Text, turn.Tools, turn.Choice), "mcp-native"), nil
}
