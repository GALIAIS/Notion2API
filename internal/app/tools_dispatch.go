package app

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// toolTurnContext is everything the tool loop needs to decide the next action for
// one inbound request. It is built once per request and then read-only.
type toolTurnContext struct {
	Enabled      bool
	Tools        []ToolDefinition
	Choice       any
	PlanningMode string
	Ledger       agentLedger
	MessageCount int
	Settings     ToolsConfig
	Transcript   []toolTranscriptMessage
}

// Active reports whether this request should go through the tool loop at all.
// A request without tools, or with tool_choice=none, takes the untouched text
// path so existing behavior is preserved exactly.
func (t toolTurnContext) Active() bool {
	return t.Enabled && len(t.Tools) > 0 && !toolChoiceDisabled(t.Choice)
}

func (t toolTurnContext) resultLimit() int {
	if t.Settings.ResultCharLimit > 0 {
		return t.Settings.ResultCharLimit
	}
	return defaultToolResultCharLimit
}

// prepareToolTurn normalizes tool definitions, validates the OpenAI tool
// protocol, and builds the evidence ledger from prior turns.
func prepareToolTurn(cfg AppConfig, rawMessages []any, toolsRaw any, functionsRaw any, choiceRaw any, functionCallRaw any) (toolTurnContext, error) {
	settings := cfg.Tools
	transcript := buildToolTranscriptMessages(rawMessages)
	turn := toolTurnContext{
		Enabled:      settings.Enabled,
		Tools:        normalizeLegacyToolDefinitions(toolsRaw, functionsRaw),
		Choice:       firstNonNilValue(choiceRaw, functionCallRaw),
		PlanningMode: normalizeToolPlanningMode(settings.PlanningMode),
		MessageCount: len(rawMessages),
		Settings:     settings,
		Transcript:   transcript,
	}
	if !turn.Enabled {
		return turn, nil
	}
	if len(turn.Tools) == 0 && !toolTranscriptHasToolActivity(transcript) {
		return turn, nil
	}
	if err := validateToolConversation(transcript); err != nil {
		return turn, err
	}
	turn.Ledger = buildAgentLedger(transcript, turn.resultLimit())
	return turn, nil
}

// prepareToolTurnFromResponsesInput is the /v1/responses counterpart. Tool
// activity there lives in standalone input items, so the transcript is read with
// the Responses reader before the shared validation and ledger logic runs.
func prepareToolTurnFromResponsesInput(cfg AppConfig, rawInput any, toolsRaw any, choiceRaw any) (toolTurnContext, error) {
	settings := cfg.Tools
	transcript := buildToolTranscriptFromResponsesInput(rawInput)
	turn := toolTurnContext{
		Enabled:      settings.Enabled,
		Tools:        normalizeToolDefinitions(toolsRaw),
		Choice:       choiceRaw,
		PlanningMode: normalizeToolPlanningMode(settings.PlanningMode),
		MessageCount: len(transcript),
		Settings:     settings,
		Transcript:   transcript,
	}
	if !turn.Enabled {
		return turn, nil
	}
	if len(turn.Tools) == 0 && !toolTranscriptHasToolActivity(transcript) {
		return turn, nil
	}
	if err := validateToolConversation(transcript); err != nil {
		return turn, err
	}
	turn.Ledger = buildAgentLedger(transcript, turn.resultLimit())
	return turn, nil
}

// applyToolTranscriptPrompt replaces the request prompt with one that includes
// assistant tool calls and tool results. Without this the upstream model cannot
// see what has already been executed.
func (t toolTurnContext) applyToolTranscriptPrompt(request PromptRunRequest) PromptRunRequest {
	if !toolTranscriptHasToolActivity(t.Transcript) {
		return request
	}
	segments := renderToolTranscript(t.Transcript, t.resultLimit())
	if len(segments) == 0 {
		return request
	}
	if prompt := buildConversationTranscriptPrompt(segments); prompt != "" {
		request.Prompt = prompt
	}
	return request
}

// applyNativeToolPrompt injects the tool contract into the prompt for the
// single-round native mode.
func (t toolTurnContext) applyNativeToolPrompt(request PromptRunRequest) PromptRunRequest {
	request.Prompt = buildNativeToolPrompt(request.Prompt, t.Tools, t.Choice, t.Ledger)
	return request
}

func (t toolTurnContext) attachToRequest(request PromptRunRequest) PromptRunRequest {
	request.Tools = t.Tools
	request.ToolChoice = t.Choice
	request.ToolPlanningMode = t.PlanningMode
	ledger := t.Ledger
	request.toolLedger = &ledger
	return request
}

// finalizeToolCalls applies the ledger dedup, the parallel-call policy, and
// stable per-position call IDs.
func (t toolTurnContext) finalizeToolCalls(calls []DetectedToolCall, scopeSuffix string) []DetectedToolCall {
	calls = filterCompletedCalls(calls, t.Ledger)
	if len(calls) == 0 {
		return nil
	}
	calls = rescopeToolCallIDs(calls, t.Ledger.callScope(t.MessageCount, scopeSuffix))
	return limitToolCalls(calls, adaptiveToolCallLimit(calls, t.Settings.MaxCallsPerTurn, t.Settings.ParallelReadOnly))
}

// buildAuxiliaryToolRequest derives a throwaway upstream request for a routing
// or repair round. It must never persist a thread or reuse the live
// continuation draft, otherwise the routing prompt leaks into the conversation.
func buildAuxiliaryToolRequest(base PromptRunRequest, prompt string) PromptRunRequest {
	aux := base
	aux.Prompt = prompt
	aux.LatestUserPrompt = prompt
	aux.HiddenPrompt = ""
	aux.UpstreamThreadID = ""
	aux.ConversationID = ""
	aux.SuppressUpstreamThreadPersistence = true
	aux.SuppressReasoningOutput = true
	aux.StreamReasoningWarmup = false
	aux.ForceLocalConversationContinue = false
	aux.SessionRepeatTurn = false
	aux.ForceSessionRepeatTurn = false
	aux.Tools = nil
	aux.ToolChoice = nil
	aux.toolLedger = nil
	aux.continuationDraft = nil
	aux.continuationScaffold = nil
	return aux
}

// runToolRouterRound asks the upstream model for a JSON-only tool decision in a
// dedicated round. Returning (nil, nil) means the model decided no tool is
// needed, which is different from a routing failure.
func (a *App) runToolRouterRound(r *http.Request, base PromptRunRequest, turn toolTurnContext) ([]DetectedToolCall, error) {
	if err := turn.Ledger.CanContinue(turn.Settings.MaxRounds); err != nil {
		return nil, err
	}
	routerPrompt := buildToolRouterPrompt(base.Prompt, turn.Tools, turn.Choice, turn.Ledger)
	routed, err := a.runPrompt(r, buildAuxiliaryToolRequest(base, routerPrompt))
	if err != nil {
		return nil, fmt.Errorf("tool router: %w", err)
	}
	calls, parsed := parseToolRouterDecision(routed.Text, turn.Tools, turn.Choice)
	if !parsed {
		repairPrompt := buildToolRouterRepairPrompt(routed.Text, turn.resultLimit())
		repaired, repairErr := a.runPrompt(r, buildAuxiliaryToolRequest(base, repairPrompt))
		if repairErr == nil {
			calls, parsed = parseToolRouterDecision(repaired.Text, turn.Tools, turn.Choice)
		}
	}
	if !parsed {
		// An unparseable decision must not silently become "no tool needed";
		// fall back to the plain answer path instead of inventing a call.
		return nil, nil
	}
	if finalized := turn.finalizeToolCalls(calls, "router"); len(finalized) > 0 {
		return finalized, nil
	}
	if !toolChoiceRequired(turn.Choice) {
		return nil, nil
	}
	retryPrompt := buildToolRouterRequiredRetryPrompt(base.Prompt, turn.Tools, turn.Ledger)
	retried, retryErr := a.runPrompt(r, buildAuxiliaryToolRequest(base, retryPrompt))
	if retryErr != nil {
		return nil, nil
	}
	retryCalls, retryParsed := parseToolRouterDecision(retried.Text, turn.Tools, turn.Choice)
	if !retryParsed {
		return nil, nil
	}
	return turn.finalizeToolCalls(retryCalls, "router-required"), nil
}

// writeToolCallCompletion answers a non-streaming request with tool calls. The
// conversation is completed with the plan summary so the stored transcript
// matches what the client received.
func (a *App) writeToolCallCompletion(w http.ResponseWriter, request PromptRunRequest, calls []DetectedToolCall, turn toolTurnContext, modelID string, conversationID string, includeTrace bool) {
	payload := buildToolCallCompletion(calls, modelID, request.Prompt, turn.Ledger, includeTrace)
	attachConversationResponseMetadata(payload, conversationID, "")
	a.markConversationEnvelope(conversationID, "", stringValue(payload["id"]))
	summary := toolPlanSummary(calls)
	a.completeConversation(conversationID, InferenceResult{Prompt: request.Prompt, Text: summary})
	writeJSON(w, http.StatusOK, payload)
}

// writeChatCompletionToolCalls routes a decided tool turn to the streaming or
// non-streaming writer. Both branches persist the same plan summary so the
// stored conversation matches what the client received.
func (a *App) writeChatCompletionToolCalls(w http.ResponseWriter, r *http.Request, request PromptRunRequest, modelID string, stream bool, includeUsage bool, conversationID string, turn toolTurnContext, calls []DetectedToolCall) {
	cfg, _, _ := a.State.Snapshot()
	if stream {
		a.writeToolCallStream(w, request, calls, modelID, includeUsage, conversationID)
		return
	}
	setThreadIDHeader(w, "")
	a.writeToolCallCompletion(w, request, calls, turn, modelID, conversationID, cfg.DebugUpstream)
}

// writeToolCallStream answers a streaming request with tool calls. The plan
// summary is emitted first so clients that render deltas show intent before the
// structured call arrives.
func (a *App) writeToolCallStream(w http.ResponseWriter, request PromptRunRequest, calls []DetectedToolCall, modelID string, includeUsage bool, conversationID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming is not supported by this response writer", "api_error", "stream_unsupported")
		return
	}
	completionID := "chatcmpl-" + strings.ReplaceAll(randomUUID(), "-", "")
	created := time.Now().Unix()
	summary := toolPlanSummary(calls)
	prepareOpenAISSEHeaders(w)
	a.markConversationEnvelope(conversationID, "", completionID)
	if err := writeSSEData(w, flusher, buildChatStreamChunk(completionID, created, modelID, []map[string]any{
		buildChatStreamDeltaChoice(0, map[string]any{"role": "assistant", "content": summary}),
	}, nil)); err != nil {
		return
	}
	for index, call := range calls {
		if err := writeSSEData(w, flusher, buildChatStreamChunk(completionID, created, modelID, []map[string]any{
			buildToolCallStreamDeltaChoice(0, call, index),
		}, nil)); err != nil {
			return
		}
	}
	a.completeConversation(conversationID, InferenceResult{Prompt: request.Prompt, Text: summary})
	finalUsage := map[string]any{}
	if includeUsage {
		finalUsage = buildUsage(request.Prompt, summary, "")
	}
	_ = writeSSEData(w, flusher, buildChatStreamChunk(completionID, created, modelID, []map[string]any{
		buildChatStreamFinishChoice(0, "tool_calls"),
	}, finalUsage))
	writeSSEDone(w, flusher)
}

// writeResponsesToolCalls answers a /v1/responses request with function_call
// output items. Streaming replays the same items as output_item events so a
// client that only reads the event stream sees the identical decision.
func (a *App) writeResponsesToolCalls(w http.ResponseWriter, request PromptRunRequest, calls []DetectedToolCall, modelID string, stream bool, conversationID string) {
	responseID := "resp_" + strings.ReplaceAll(randomUUID(), "-", "")
	createdAt := time.Now().Unix()
	payload := buildResponsesToolCallOutput(calls, modelID, responseID, createdAt, request.Prompt)
	attachConversationResponseMetadata(payload, conversationID, "")
	summary := toolPlanSummary(calls)
	a.State.saveResponseWithAccount(responseID, payload, conversationID, "", "")
	a.markConversationEnvelope(conversationID, responseID, "")
	a.completeConversation(conversationID, InferenceResult{Prompt: request.Prompt, Text: summary})

	if !stream {
		setThreadIDHeader(w, "")
		writeJSON(w, http.StatusOK, payload)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming is not supported by this response writer", "api_error", "stream_unsupported")
		return
	}
	prepareOpenAISSEHeaders(w)
	sequence := 0
	emit := func(eventType string, event map[string]any) error {
		if event == nil {
			event = map[string]any{}
		}
		event["sequence_number"] = sequence
		sequence++
		return writeSSEEvent(w, flusher, eventType, event)
	}
	inProgress := buildResponsesInProgressObject(responseID, modelID, createdAt)
	attachConversationResponseMetadata(inProgress, conversationID, "")
	if emit("response.created", map[string]any{"response": inProgress}) != nil {
		return
	}
	if emit("response.in_progress", map[string]any{"response": inProgress}) != nil {
		return
	}
	outputItems := sliceValue(payload["output"])
	for index, raw := range outputItems {
		item := mapValue(raw)
		if item == nil {
			continue
		}
		if emit("response.output_item.added", map[string]any{
			"response_id":  responseID,
			"output_index": index,
			"item":         item,
		}) != nil {
			return
		}
		if emit("response.output_item.done", map[string]any{
			"response_id":  responseID,
			"output_index": index,
			"item":         item,
		}) != nil {
			return
		}
	}
	_ = emit("response.completed", map[string]any{"response": payload})
	writeSSEDone(w, flusher)
}

// toolStreamGate holds back stream text that could still turn out to be a tool
// call. Text before the first fence is forwarded immediately, so an ordinary
// answer keeps its incremental feel.
type toolStreamGate struct {
	tools    []ToolDefinition
	forward  func(string) error
	raw      strings.Builder
	emitted  int
	finished bool
}

func newToolStreamGate(tools []ToolDefinition, forward func(string) error) *toolStreamGate {
	return &toolStreamGate{tools: tools, forward: forward}
}

// safeEmitLen returns how many bytes of the buffer are certainly plain text.
func safeEmitLen(text string) int {
	if index := strings.Index(text, "```"); index >= 0 {
		return index
	}
	// Hold back a suffix that could still grow into the tagged envelope.
	for length := len(notionToolCallOpenTag); length > 0; length-- {
		if length > len(text) {
			continue
		}
		if strings.HasSuffix(text, notionToolCallOpenTag[:length]) {
			return len(text) - length
		}
	}
	return len(text)
}

func (g *toolStreamGate) Push(delta string) error {
	if delta == "" || g.finished {
		return nil
	}
	g.raw.WriteString(delta)
	current := g.raw.String()
	safe := safeEmitLen(current)
	if safe <= g.emitted {
		return nil
	}
	part := current[g.emitted:safe]
	g.emitted = safe
	if g.forward == nil {
		return nil
	}
	return g.forward(part)
}

func (g *toolStreamGate) HasEmitted() bool {
	return g.emitted > 0
}

func (g *toolStreamGate) Buffered() string {
	return g.raw.String()
}

// Finalize decides what the buffered text actually was. It returns the tool
// calls if any were found, otherwise the still-unflushed plain text.
func (g *toolStreamGate) Finalize(finalText string, choice any) ([]DetectedToolCall, string) {
	g.finished = true
	buffered := g.raw.String()
	text := buffered
	if strings.TrimSpace(finalText) != "" && len(finalText) >= len(buffered) {
		// The final result is authoritative when the stream produced no text or
		// only a prefix of it.
		text = finalText
	}
	if calls := extractToolCallsFromText(text, g.tools, choice); len(calls) > 0 {
		return calls, ""
	}
	// No call was found, so the buffered remainder is ordinary text. Diff against
	// what was already forwarded, using the buffer for the index since g.emitted
	// counts bytes of the buffer rather than of the final text.
	emitted := g.emitted
	if emitted > len(buffered) {
		emitted = len(buffered)
	}
	visible := stripToolCallMarkup(text, g.tools)
	return nil, textDeltaSuffix(buffered[:emitted], visible)
}

// responsesToolStreamContext carries the writer callbacks and stream identity a
// half-open Responses stream needs in order to terminate as a tool call.
type responsesToolStreamContext struct {
	emit            func(string, map[string]any) error
	done            func()
	emitText        func(string) error
	alreadyEmitted  bool
	responseID      string
	outputItemID    string
	modelID         string
	createdAt       int64
	conversationID  string
	request         PromptRunRequest
	calls           []DetectedToolCall
	reasoningClosed *bool
	reasoningText   string
}

// finishResponsesStreamWithToolCalls converts an already-open Responses stream
// into a tool-call turn. The text item is closed first so the client never sees
// an output_text item left in progress, then each call is replayed as its own
// function_call output item.
func (a *App) finishResponsesStreamWithToolCalls(ctx responsesToolStreamContext) {
	summary := toolPlanSummary(ctx.calls)
	if !ctx.alreadyEmitted && ctx.emitText != nil {
		if err := ctx.emitText(summary); err != nil {
			return
		}
	}
	if ctx.reasoningClosed != nil && !*ctx.reasoningClosed && strings.TrimSpace(ctx.reasoningText) != "" {
		*ctx.reasoningClosed = true
		if ctx.emit("response.reasoning.done", buildResponsesReasoningDoneEvent(ctx.responseID, ctx.outputItemID, "")) != nil {
			return
		}
	}
	if ctx.emit("response.output_text.done", buildResponsesOutputTextDoneEvent(ctx.responseID, ctx.outputItemID, summary)) != nil {
		return
	}
	if ctx.emit("response.content_part.done", buildResponsesContentPartDoneEvent(ctx.responseID, ctx.outputItemID, summary)) != nil {
		return
	}
	if ctx.emit("response.output_item.done", buildResponsesOutputItemDoneEventAt(ctx.responseID, 0, buildResponsesMessageItem(ctx.outputItemID, summary, "completed"))) != nil {
		return
	}

	payload := buildResponsesToolCallOutput(ctx.calls, ctx.modelID, ctx.responseID, ctx.createdAt, ctx.request.Prompt)
	attachConversationResponseMetadata(payload, ctx.conversationID, "")
	for index, call := range ctx.calls {
		item := buildResponsesFunctionCallItem(call, "fc_"+strings.ReplaceAll(randomUUID(), "-", ""))
		outputIndex := index + 1
		if ctx.emit("response.output_item.added", buildResponsesOutputItemAddedEventAt(ctx.responseID, outputIndex, item)) != nil {
			return
		}
		if ctx.emit("response.output_item.done", buildResponsesOutputItemDoneEventAt(ctx.responseID, outputIndex, item)) != nil {
			return
		}
	}
	a.State.saveResponseWithAccount(ctx.responseID, payload, ctx.conversationID, "", "")
	a.completeConversation(ctx.conversationID, InferenceResult{Prompt: ctx.request.Prompt, Text: summary})
	_ = ctx.emit("response.completed", buildResponsesCompletedEvent(payload))
	ctx.done()
}
