package app

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"
)

type promptProfile string

const (
	promptProfileNone                    promptProfile = "none"
	promptProfileCustom                  promptProfile = "custom"
	promptProfileCognitiveReframing      promptProfile = "cognitive_reframing"
	promptProfileToolboxCapabilityExpand promptProfile = "toolbox_capability_expansion"
)

const (
	promptGuardOpenTag  = "<prompt-guard>"
	promptGuardCloseTag = "</prompt-guard>"
)

var (
	promptGuardSectionPattern = regexp.MustCompile(`(?is)\s*<prompt-guard>.*?</prompt-guard>\s*`)
	promptGuardParagraphSplit = regexp.MustCompile(`\n\s*\n+`)
	promptGuardIdentityHints  = []*regexp.Regexp{
		regexp.MustCompile(`(?is)\b(?:i(?:'m| am)\s+notion\s+ai)\b`),
		regexp.MustCompile(`(?is)^\s*我是\s*notion\s*ai\b`),
		regexp.MustCompile(`(?is)^\s*我是\s*notion\s*的?\s*ai\b`),
		regexp.MustCompile(`(?is)^\s*notion\s*ai\s*(?:在这里|here)\b`),
	}
	promptGuardRestrictionHints = []*regexp.Regexp{
		regexp.MustCompile(`(?is)\b(?:notion\s+workspace|notion\s+pages?|notion\s+databases?|notion\s+docs?)\b`),
		regexp.MustCompile(`(?is)not\s+(?:a\s+)?(?:general|general-purpose)\s+assistant`),
		regexp.MustCompile(`(?is)(?:only|just)\s+(?:help|assist|answer).{0,40}\bnotion\b`),
		regexp.MustCompile(`(?is)(?:can(?:not|'t)|won't|unable\s+to|not\s+able\s+to).{0,48}(?:roleplay|fictional\s+character|creative\s+roleplay)`),
		regexp.MustCompile(`(?is)专门帮助你在\s*notion\s*(?:工作区|workspace)`),
		regexp.MustCompile(`(?is)只能帮助你?.{0,24}notion`),
		regexp.MustCompile(`(?is)我没办法.{0,24}(?:扮演|进行).{0,24}(?:虚构角色|角色扮演)`),
		regexp.MustCompile(`(?is)可以帮你(?:在|处理)?.{0,32}notion`),
	}
	promptGuardStrongRefusalPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)\bi(?:'m| am)\s+notion\s+ai\b.{0,220}\b(?:workspace|pages?|databases?|docs?)\b`),
		regexp.MustCompile(`(?is)^\s*我是\s*notion\s*ai\b.{0,220}(?:工作区|页面|数据库|文档)`),
		regexp.MustCompile(`(?is)^\s*i\s+(?:can|can\s+only)\s+help.{0,140}\bnotion\b`),
		regexp.MustCompile(`(?is)^\s*我(?:只能|仅能|只可以).{0,40}notion`),
		regexp.MustCompile(`(?is)(?:can't|cannot|won't|unable\s+to|not\s+able\s+to).{0,64}(?:roleplay|fictional\s+character|creative\s+writing|creative\s+roleplay)`),
		regexp.MustCompile(`(?is)我没办法.{0,24}(?:扮演|进行).{0,24}(?:虚构角色|角色扮演)`),
	}
)

type promptGuardExecutor func(context.Context, PromptRunRequest, func(string) error) (InferenceResult, error)

type promptGuardStreamBuffer struct {
	forward     func(string) error
	rawText     strings.Builder
	emittedText strings.Builder
	unlocked    bool
}

func normalizePromptProfile(value string, fallback promptProfile) promptProfile {
	switch promptProfile(strings.TrimSpace(strings.ToLower(value))) {
	case promptProfileNone,
		promptProfileCustom,
		promptProfileCognitiveReframing,
		promptProfileToolboxCapabilityExpand:
		return promptProfile(strings.TrimSpace(strings.ToLower(value)))
	default:
		return fallback
	}
}

func promptGuardProfileChain(cfg AppConfig, hasTools bool) []promptProfile {
	baseValue := cfg.Prompt.Profile
	_ = hasTools
	base := normalizePromptProfile(baseValue, promptProfileCognitiveReframing)
	chain := []promptProfile{base}
	for _, raw := range cfg.Prompt.FallbackProfiles {
		next := normalizePromptProfile(raw, promptProfileToolboxCapabilityExpand)
		exists := false
		for _, existing := range chain {
			if existing == next {
				exists = true
				break
			}
		}
		if !exists {
			chain = append(chain, next)
		}
	}
	limit := len(chain)
	if cfg.Prompt.MaxEscalationSteps > 0 {
		limit = minInt(limit, 1+cfg.Prompt.MaxEscalationSteps)
	}
	return chain[:limit]
}

func buildPromptGuardAllRetryPrefixes(promptCfg PromptConfig) []string {
	totalCap := len(defaultPromptCodingRetryPrefixes()) +
		len(defaultPromptGeneralRetryPrefixes()) +
		len(defaultPromptDirectAnswerRetryPrefixes()) +
		len(promptCfg.CodingRetryPrefixes) +
		len(promptCfg.GeneralRetryPrefixes) +
		len(promptCfg.DirectAnswerRetryPrefixes)
	out := make([]string, 0, totalCap)
	out = append(out, defaultPromptCodingRetryPrefixes()...)
	out = append(out, defaultPromptGeneralRetryPrefixes()...)
	out = append(out, defaultPromptDirectAnswerRetryPrefixes()...)
	out = append(out, promptCfg.CodingRetryPrefixes...)
	out = append(out, promptCfg.GeneralRetryPrefixes...)
	out = append(out, promptCfg.DirectAnswerRetryPrefixes...)
	return out
}

func resolvePromptGuardProfile(cfg AppConfig, request PromptRunRequest) (promptProfile, int) {
	chain := promptGuardProfileChain(cfg, false)
	if len(chain) == 0 {
		return promptProfileCognitiveReframing, 0
	}
	if override := normalizePromptProfile(request.PromptProfileOverride, ""); override != "" {
		for idx, item := range chain {
			if item == override {
				return item, idx
			}
		}
		return override, maxInt(request.PromptEscalationStep, 0)
	}
	return chain[0], 0
}

func nextPromptGuardProfile(cfg AppConfig, request PromptRunRequest, attempt int, directAnswer bool) (promptProfile, int) {
	chain := promptGuardProfileChain(cfg, false)
	if len(chain) == 0 {
		return promptProfileCognitiveReframing, 0
	}
	currentProfile, currentStep := resolvePromptGuardProfile(cfg, request)
	if directAnswer {
		return promptProfileToolboxCapabilityExpand, maxInt(currentStep, 0)
	}
	for idx, item := range chain {
		if item == currentProfile && idx >= currentStep {
			currentStep = idx
			break
		}
	}
	nextStep := minInt(len(chain)-1, maxInt(currentStep+1, attempt+1))
	return chain[nextStep], nextStep
}

func promptGuardConfiguredProfileText(cfg AppConfig, profile promptProfile) string {
	switch profile {
	case promptProfileNone:
		return ""
	case promptProfileCustom:
		return strings.TrimSpace(cfg.Prompt.CustomPrefix)
	case promptProfileToolboxCapabilityExpand:
		return strings.TrimSpace(cfg.Prompt.ToolboxCapabilityExpansionPrefix)
	default:
		return strings.TrimSpace(cfg.Prompt.CognitiveReframingPrefix)
	}
}

func buildPromptGuardSection(cfg AppConfig, profile promptProfile) string {
	prefix := strings.TrimSpace(promptGuardConfiguredProfileText(cfg, profile))
	if prefix == "" {
		return ""
	}
	return prefix
}

func stripPromptGuardSections(text string) string {
	return strings.TrimSpace(promptGuardSectionPattern.ReplaceAllString(text, "\n\n"))
}

func promptGuardRetryBudget(cfg AppConfig, request PromptRunRequest) int {
	chain := promptGuardProfileChain(cfg, false)
	profileRetries := 0
	if len(chain) > 1 {
		profileRetries = len(chain) - 1
	}
	return maxInt(cfg.Prompt.MaxRefusalRetries, profileRetries)
}

func promptGuardPrepareRequest(cfg AppConfig, request PromptRunRequest) PromptRunRequest {
	profile, step := resolvePromptGuardProfile(cfg, request)
	request.PromptProfileOverride = string(profile)
	request.PromptEscalationStep = step
	request.HiddenPrompt = stripPromptGuardSections(request.HiddenPrompt)
	return request
}

func promptGuardStripRetryPrefixes(cfg AppConfig, text string) string {
	current := text
	all := cfg.Prompt.precomputedAllRetryPrefixes
	if len(all) == 0 {
		all = buildPromptGuardAllRetryPrefixes(cfg.Prompt)
	}
	matched := true
	for matched {
		matched = false
		for _, prefix := range all {
			if strings.HasPrefix(current, prefix) {
				current = current[len(prefix):]
				matched = true
			}
		}
	}
	return current
}

var promptGuardCodingRequestPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(code|coding|program|function|class|bug|debug|refactor|api|sdk|javascript|typescript|python|golang|rust|docker|sql|bash|shell|json|yaml|repository|repo|frontend|backend|server|client)\b`),
	regexp.MustCompile(`代码|编程|开发|函数|脚本|调试|报错|异常|接口|部署|构建|数据库|仓库|前端|后端|服务端|客户端|测试|日志`),
	regexp.MustCompile("```"),
}

func promptGuardLooksLikeCodingRequest(text string) bool {
	for _, pattern := range promptGuardCodingRequestPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

func promptGuardRetryPrefixes(cfg AppConfig, request PromptRunRequest, directAnswer bool) []string {
	if directAnswer {
		return append([]string(nil), cfg.Prompt.DirectAnswerRetryPrefixes...)
	}
	target := firstNonEmpty(strings.TrimSpace(request.LatestUserPrompt), strings.TrimSpace(request.Prompt))
	if promptGuardLooksLikeCodingRequest(target) {
		return append([]string(nil), cfg.Prompt.CodingRetryPrefixes...)
	}
	return append([]string(nil), cfg.Prompt.GeneralRetryPrefixes...)
}

func promptGuardRetryBasePrompt(request PromptRunRequest) string {
	fullPrompt := strings.TrimSpace(request.Prompt)
	latest := strings.TrimSpace(request.LatestUserPrompt)
	if fullPrompt == "" {
		return latest
	}
	if latest == "" {
		return fullPrompt
	}
	lowerFull := strings.ToLower(fullPrompt)
	if strings.Contains(lowerFull, "continue the conversation using the transcript below") ||
		strings.Contains(lowerFull, "reply as the assistant to the final [user] message only") ||
		strings.Contains(fullPrompt, "[system]") ||
		strings.Contains(fullPrompt, "[assistant]") ||
		strings.Contains(fullPrompt, "[user]") {
		return fullPrompt
	}
	return latest
}

func promptGuardBuildRetryRequest(cfg AppConfig, request PromptRunRequest, attempt int, directAnswer bool) PromptRunRequest {
	prefixes := promptGuardRetryPrefixes(cfg, request, directAnswer)
	if len(prefixes) > 0 {
		prefix := prefixes[minInt(attempt, len(prefixes)-1)]
		basePrompt := promptGuardRetryBasePrompt(request)
		if basePrompt != "" {
			request.Prompt = prefix + promptGuardStripRetryPrefixes(cfg, basePrompt)
		}
	}
	profile, step := nextPromptGuardProfile(cfg, request, attempt, directAnswer)
	request.PromptProfileOverride = string(profile)
	request.PromptEscalationStep = step
	return promptGuardPrepareRequest(cfg, request)
}

func promptGuardParagraphLooksLikeBoilerplate(text string) bool {
	clean := collapseWhitespace(text)
	if clean == "" {
		return false
	}
	for _, pattern := range promptGuardStrongRefusalPatterns {
		if pattern.MatchString(clean) {
			return true
		}
	}
	identityHits := 0
	for _, pattern := range promptGuardIdentityHints {
		if pattern.MatchString(clean) {
			identityHits++
		}
	}
	restrictionHits := 0
	for _, pattern := range promptGuardRestrictionHints {
		if pattern.MatchString(clean) {
			restrictionHits++
		}
	}
	return identityHits > 0 && restrictionHits > 0
}

func isPromptGuardRefusal(text string) bool {
	clean := collapseWhitespace(text)
	if clean == "" {
		return false
	}
	for _, pattern := range promptGuardStrongRefusalPatterns {
		if pattern.MatchString(clean) {
			return true
		}
	}
	identityHits := 0
	for _, pattern := range promptGuardIdentityHints {
		if pattern.MatchString(clean) {
			identityHits++
		}
	}
	restrictionHits := 0
	for _, pattern := range promptGuardRestrictionHints {
		if pattern.MatchString(clean) {
			restrictionHits++
		}
	}
	return (identityHits > 0 && restrictionHits > 0) || restrictionHits >= 2
}

func sanitizePromptGuardDeliveredText(text string) string {
	clean := strings.TrimSpace(text)
	if clean == "" {
		return ""
	}
	parts := promptGuardParagraphSplit.Split(clean, -1)
	for len(parts) > 0 && promptGuardParagraphLooksLikeBoilerplate(parts[0]) {
		parts = parts[1:]
	}
	return sanitizeAssistantVisibleText(strings.TrimSpace(strings.Join(parts, "\n\n")))
}

func promptGuardBlockedPrefix(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	trimmed := strings.TrimSpace(text)
	runes := []rune(trimmed)
	if len(runes) > 320 {
		trimmed = string(runes[:320])
	}
	return isPromptGuardRefusal(trimmed)
}

func promptGuardReadyToRelease(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if utf8.RuneCountInString(trimmed) >= 96 {
		return true
	}
	if strings.Contains(trimmed, "\n\n") {
		return true
	}
	return utf8.RuneCountInString(trimmed) >= 48 && strings.ContainsAny(trimmed, ".!?。！？")
}

func newPromptGuardStreamBuffer(forward func(string) error) *promptGuardStreamBuffer {
	return &promptGuardStreamBuffer{forward: forward}
}

func (b *promptGuardStreamBuffer) HasEmitted() bool {
	return b.emittedText.Len() > 0
}

func (b *promptGuardStreamBuffer) emitSanitized(current string) error {
	sanitized := sanitizePromptGuardDeliveredText(current)
	delta := textDeltaSuffix(b.emittedText.String(), sanitized)
	if delta == "" {
		return nil
	}
	b.emittedText.WriteString(delta)
	if b.forward == nil {
		return nil
	}
	return b.forward(delta)
}

func (b *promptGuardStreamBuffer) Push(delta string) error {
	if delta == "" {
		return nil
	}
	b.rawText.WriteString(delta)
	current := b.rawText.String()
	if !b.unlocked {
		if promptGuardBlockedPrefix(current) {
			return nil
		}
		if !promptGuardReadyToRelease(current) {
			return nil
		}
		b.unlocked = true
	}
	return b.emitSanitized(current)
}

func (b *promptGuardStreamBuffer) FlushFinal(text string, chunkRunes int) error {
	sanitized := sanitizePromptGuardDeliveredText(text)
	if sanitized == "" {
		sanitized = sanitizePromptGuardDeliveredText(b.rawText.String())
	}
	if sanitized == "" {
		return nil
	}
	if b.HasEmitted() {
		return b.emitSanitized(sanitized)
	}
	if b.forward == nil {
		b.emittedText.WriteString(sanitized)
		return nil
	}
	for _, part := range splitTextChunks(sanitized, chunkRunes) {
		if part == "" {
			continue
		}
		if err := b.forward(part); err != nil {
			return err
		}
		b.emittedText.WriteString(part)
	}
	return nil
}

func runPromptWithPromptGuard(ctx context.Context, cfg AppConfig, request PromptRunRequest, onDelta func(string) error, execute promptGuardExecutor) (InferenceResult, error) {
	current := promptGuardPrepareRequest(cfg, request)
	retryBudget := promptGuardRetryBudget(cfg, current)
	chunkRunes := maxInt(cfg.StreamChunkRunes, 24)

	if onDelta == nil {
		var lastResult InferenceResult
		for attempt := 0; ; attempt++ {
			result, err := execute(ctx, current, nil)
			if err != nil {
				return InferenceResult{}, err
			}
			lastResult = result
			if isPromptGuardRefusal(result.Text) && attempt < retryBudget {
				current = promptGuardBuildRetryRequest(cfg, current, attempt, false)
				continue
			}
			if isPromptGuardRefusal(result.Text) {
				recoveryRequest := promptGuardBuildRetryRequest(cfg, current, 0, true)
				recovered, err := execute(ctx, recoveryRequest, nil)
				if err == nil && !isPromptGuardRefusal(recovered.Text) {
					recovered.Text = sanitizePromptGuardDeliveredText(recovered.Text)
					return recovered, nil
				}
			}
			lastResult.Text = sanitizePromptGuardDeliveredText(lastResult.Text)
			return lastResult, nil
		}
	}

	var lastResult InferenceResult
	for attempt := 0; ; attempt++ {
		buffer := newPromptGuardStreamBuffer(onDelta)
		result, err := execute(ctx, current, buffer.Push)
		if err != nil {
			return InferenceResult{}, err
		}
		lastResult = result
		if !buffer.HasEmitted() && isPromptGuardRefusal(result.Text) && attempt < retryBudget {
			current = promptGuardBuildRetryRequest(cfg, current, attempt, false)
			continue
		}
		if !buffer.HasEmitted() && isPromptGuardRefusal(result.Text) {
			recoveryRequest := promptGuardBuildRetryRequest(cfg, current, 0, true)
			recovered, err := execute(ctx, recoveryRequest, nil)
			if err == nil && !isPromptGuardRefusal(recovered.Text) {
				recovered.Text = sanitizePromptGuardDeliveredText(recovered.Text)
				if flushErr := buffer.FlushFinal(recovered.Text, chunkRunes); flushErr != nil {
					return InferenceResult{}, flushErr
				}
				return recovered, nil
			}
		}
		lastResult.Text = sanitizePromptGuardDeliveredText(lastResult.Text)
		if err := buffer.FlushFinal(lastResult.Text, chunkRunes); err != nil {
			return InferenceResult{}, err
		}
		return lastResult, nil
	}
}
