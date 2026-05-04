package app

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode"
)

type ModelRegistry struct {
	Entries   []ModelDefinition
	ByID      map[string]ModelDefinition
	AliasToID map[string]string
}

type probeModelsEnvelope struct {
	Models []struct {
		Model        string `json:"model"`
		ModelMessage string `json:"modelMessage"`
		ModelFamily  string `json:"modelFamily"`
		DisplayGroup string `json:"displayGroup"`
		IsDisabled   bool   `json:"isDisabled"`
		MarkdownChat struct {
			Beta bool `json:"beta"`
		} `json:"markdownChat"`
		Workflow struct {
			FinalModelName string `json:"finalModelName"`
			Beta           bool   `json:"beta"`
		} `json:"workflow"`
		CustomAgent struct {
			FinalModelName string `json:"finalModelName"`
			Beta           bool   `json:"beta"`
		} `json:"customAgent"`
	} `json:"models"`
}

func builtinModelDefinitions() []ModelDefinition {
	return []ModelDefinition{
		{ID: "auto", Name: "Auto", NotionModel: "", Family: "system", Group: "default", Enabled: true, Aliases: []string{"default", "workflow", "notion-ai-workflow"}},
		{ID: "gpt-5.2", Name: "GPT-5.2", NotionModel: "oatmeal-cookie", Family: "openai", Group: "fast", Beta: true, Enabled: true, Aliases: []string{"gpt52", "oatmeal-cookie"}},
		{ID: "gpt-5.4", Name: "GPT-5.4", NotionModel: "oval-kumquat-medium", Family: "openai", Group: "fast", Beta: true, Enabled: true, Aliases: []string{"gpt54", "oval-kumquat-medium"}},
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", NotionModel: "vertex-gemini-2.5-flash", Family: "gemini", Group: "fast", Enabled: true, Aliases: []string{"vertex-gemini-2.5-flash"}},
		{ID: "gemini-3.1-pro", Name: "Gemini 3.1 Pro", NotionModel: "galette-medium-thinking", Family: "gemini", Group: "intelligent", Beta: true, Enabled: true, Aliases: []string{"gemini31pro", "galette-medium-thinking"}},
		{ID: "sonnet-4.6", Name: "Sonnet 4.6", NotionModel: "almond-croissant-low", Family: "anthropic", Group: "fast", Beta: true, Enabled: true, Aliases: []string{"claude-sonnet-4.6", "almond-croissant-low"}},
		{ID: "opus-4.7", Name: "Opus 4.7", NotionModel: "apricot-sorbet-medium", Family: "anthropic", Group: "intelligent", Beta: true, Enabled: true, Aliases: []string{"claude-opus-4.7", "opus47", "apricot-sorbet-medium"}},
		{ID: "opus-4.6", Name: "Opus 4.6", NotionModel: "avocado-froyo-medium", Family: "anthropic", Group: "intelligent", Beta: true, Enabled: true, Aliases: []string{"claude-opus-4.6", "avocado-froyo-medium"}},
		{ID: "gpt-5.4-mini", Name: "GPT-5.4 Mini", NotionModel: "oregon-grape-medium", Family: "openai", Group: "fast", Beta: true, Enabled: true, Aliases: []string{"oregon-grape-medium"}},
		{ID: "gpt-5.4-nano", Name: "GPT-5.4 Nano", NotionModel: "otaheite-apple-medium", Family: "openai", Group: "fast", Beta: true, Enabled: true, Aliases: []string{"otaheite-apple-medium"}},
		{ID: "minimax-m2.5", Name: "MiniMax M2.5", NotionModel: "fireworks-minimax-m2.5", Family: "mystery", Group: "intelligent", Enabled: true, Aliases: []string{"fireworks-minimax-m2.5"}},
		{ID: "haiku-4.5", Name: "Haiku 4.5", NotionModel: "anthropic-haiku-4.5", Family: "anthropic", Group: "fast", Enabled: true, Aliases: []string{"claude-haiku-4.5", "anthropic-haiku-4.5"}},
		{ID: "gemini-3-flash", Name: "Gemini 3 Flash", NotionModel: "gingerbread", Family: "gemini", Group: "fast", Enabled: true, Aliases: []string{"gingerbread"}},
	}
}

func buildModelRegistry(cfg AppConfig) ModelRegistry {
	entries := builtinModelDefinitions()
	if probeEntries := extractProbeModelDefinitions(collectProbeModelPaths(cfg)); len(probeEntries) > 0 {
		entries = mergeModelDefinitions(entries, probeEntries)
	}
	if len(cfg.Models) > 0 {
		entries = mergeModelDefinitions(entries, cfg.Models)
	}
	entries = ensureAutoModel(entries)

	byID := map[string]ModelDefinition{}
	aliasToID := map[string]string{}
	for i := range entries {
		entry := entries[i]
		entry = normalizeModelDefinition(entry)
		entries[i] = entry
		if !entry.Enabled {
			continue
		}
		byID[entry.ID] = entry
		registerModelAlias(aliasToID, entry.ID, entry.ID)
		registerModelAlias(aliasToID, entry.Name, entry.ID)
		registerModelAlias(aliasToID, entry.NotionModel, entry.ID)
		for _, alias := range entry.Aliases {
			registerModelAlias(aliasToID, alias, entry.ID)
		}
	}
	for alias, target := range cfg.ModelAliases {
		canonicalTarget := strings.TrimSpace(target)
		if canonicalTarget == "" {
			continue
		}
		canonicalTarget = normalizeLookupKey(canonicalTarget)
		if resolved, ok := aliasToID[canonicalTarget]; ok {
			canonicalTarget = resolved
		}
		if _, ok := byID[canonicalTarget]; ok {
			registerModelAlias(aliasToID, alias, canonicalTarget)
		}
	}
	entries = sortedModelEntries(entries)
	return ModelRegistry{Entries: entries, ByID: byID, AliasToID: aliasToID}
}

func collectProbeModelPaths(cfg AppConfig) []string {
	paths := make([]string, 0, len(cfg.Accounts)+1)
	seen := map[string]struct{}{}
	appendPath := func(path string) {
		clean := strings.TrimSpace(path)
		if clean == "" {
			return
		}
		if _, exists := seen[clean]; exists {
			return
		}
		seen[clean] = struct{}{}
		paths = append(paths, clean)
	}
	appendPath(cfg.ProbeJSON)
	for _, account := range cfg.Accounts {
		appendPath(account.ProbeJSON)
	}
	return paths
}

func (r ModelRegistry) Resolve(value string, fallback string) (ModelDefinition, error) {
	candidate := strings.TrimSpace(value)
	if candidate == "" {
		candidate = fallback
	}
	if candidate == "" {
		candidate = "auto"
	}
	key := normalizeLookupKey(candidate)
	if id, ok := r.AliasToID[key]; ok {
		if entry, ok := r.ByID[id]; ok {
			return entry, nil
		}
	}
	if entry, ok := r.ByID[key]; ok {
		return entry, nil
	}
	return ModelDefinition{}, fmt.Errorf("unknown model: %s", candidate)
}

func extractProbeModelDefinitions(paths []string) []ModelDefinition {
	if len(paths) == 0 {
		return nil
	}
	parseConcurrency := len(paths)
	if parseConcurrency > 4 {
		parseConcurrency = 4
	}
	if parseConcurrency < 1 {
		parseConcurrency = 1
	}
	type indexedResult struct {
		index int
		items []ModelDefinition
	}
	results := make([]indexedResult, len(paths))
	sem := make(chan struct{}, parseConcurrency)
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func(index int, probePath string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[index] = indexedResult{
				index: index,
				items: extractProbeModelDefinitionsFromPath(probePath),
			}
		}(i, path)
	}
	wg.Wait()

	seen := map[string]struct{}{}
	out := make([]ModelDefinition, 0)
	for _, result := range results {
		for _, item := range result.items {
			key := item.ID + "|" + item.NotionModel
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

func extractProbeModelDefinitionsFromPath(path string) []ModelDefinition {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []ModelDefinition
	walkProbeValues(data, func(text string) {
		parsed := parseProbeModelsBlob(text)
		for _, item := range parsed {
			key := item.ID + "|" + item.NotionModel
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, item)
		}
	})
	return out
}

func walkProbeValues(node any, visit func(string)) {
	switch x := node.(type) {
	case map[string]any:
		for _, value := range x {
			walkProbeValues(value, visit)
		}
	case []any:
		for _, value := range x {
			walkProbeValues(value, visit)
		}
	case string:
		visit(x)
	}
}

func parseProbeModelsBlob(text string) []ModelDefinition {
	if !strings.Contains(text, `"models"`) || !strings.Contains(text, `modelMessage`) {
		return nil
	}
	var payload probeModelsEnvelope
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return nil
	}
	out := make([]ModelDefinition, 0, len(payload.Models))
	for _, model := range payload.Models {
		name := strings.TrimSpace(model.ModelMessage)
		if name == "" {
			name = strings.TrimSpace(model.Model)
		}
		notionModel := strings.TrimSpace(model.Workflow.FinalModelName)
		if notionModel == "" {
			notionModel = strings.TrimSpace(model.CustomAgent.FinalModelName)
		}
		if notionModel == "" {
			notionModel = strings.TrimSpace(model.Model)
		}
		entry := normalizeModelDefinition(ModelDefinition{
			ID:          slugModelID(name),
			Name:        name,
			NotionModel: notionModel,
			Family:      strings.TrimSpace(model.ModelFamily),
			Group:       strings.TrimSpace(model.DisplayGroup),
			Beta:        model.MarkdownChat.Beta || model.Workflow.Beta || model.CustomAgent.Beta,
			Enabled:     !model.IsDisabled,
			Aliases:     []string{model.Model, notionModel},
		})
		if entry.ID != "" {
			out = append(out, entry)
		}
	}
	return out
}

func mergeModelDefinitions(base []ModelDefinition, incoming []ModelDefinition) []ModelDefinition {
	merged := append([]ModelDefinition{}, base...)
	for _, rawEntry := range incoming {
		entry := normalizeModelDefinition(rawEntry)
		if entry.ID == "" && entry.NotionModel == "" {
			continue
		}
		replaced := false
		for i, current := range merged {
			if sameModelDefinition(current, entry) {
				merged[i] = mergeSingleModelDefinition(current, entry)
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, entry)
		}
	}
	return merged
}

func mergeSingleModelDefinition(base ModelDefinition, incoming ModelDefinition) ModelDefinition {
	out := normalizeModelDefinition(base)
	candidate := normalizeModelDefinition(incoming)
	if candidate.ID != "" {
		out.ID = candidate.ID
	}
	if candidate.Name != "" {
		out.Name = candidate.Name
	}
	if candidate.NotionModel != "" {
		out.NotionModel = candidate.NotionModel
	}
	if candidate.Family != "" {
		out.Family = candidate.Family
	}
	if candidate.Group != "" {
		out.Group = candidate.Group
	}
	out.Beta = candidate.Beta || out.Beta
	out.Enabled = candidate.Enabled
	aliasSet := map[string]struct{}{}
	var aliases []string
	for _, value := range append(out.Aliases, candidate.Aliases...) {
		key := normalizeLookupKey(value)
		if key == "" {
			continue
		}
		if _, ok := aliasSet[key]; ok {
			continue
		}
		aliasSet[key] = struct{}{}
		aliases = append(aliases, strings.TrimSpace(value))
	}
	out.Aliases = aliases
	return out
}

func sameModelDefinition(a ModelDefinition, b ModelDefinition) bool {
	if normalizeLookupKey(a.ID) != "" && normalizeLookupKey(a.ID) == normalizeLookupKey(b.ID) {
		return true
	}
	if normalizeLookupKey(a.NotionModel) != "" && normalizeLookupKey(a.NotionModel) == normalizeLookupKey(b.NotionModel) {
		return true
	}
	return false
}

func ensureAutoModel(entries []ModelDefinition) []ModelDefinition {
	for _, entry := range entries {
		if normalizeLookupKey(entry.ID) == "auto" {
			return entries
		}
	}
	return append([]ModelDefinition{{ID: "auto", Name: "Auto", Enabled: true, Family: "system", Group: "default"}}, entries...)
}

func normalizeModelDefinition(entry ModelDefinition) ModelDefinition {
	entry.ID = slugModelID(strings.TrimSpace(entry.ID))
	if entry.ID == "" {
		entry.ID = slugModelID(strings.TrimSpace(entry.Name))
	}
	entry.Name = strings.TrimSpace(entry.Name)
	if entry.Name == "" && entry.ID != "" {
		entry.Name = entry.ID
	}
	entry.NotionModel = strings.TrimSpace(entry.NotionModel)
	entry.Family = strings.TrimSpace(entry.Family)
	entry.Group = strings.TrimSpace(entry.Group)
	if entry.ID == "auto" {
		entry.NotionModel = ""
		entry.Enabled = true
	}
	if !entry.Enabled && entry.ID == "" && entry.NotionModel == "" {
		entry.Enabled = true
	}
	aliasSet := map[string]struct{}{}
	var aliases []string
	for _, alias := range entry.Aliases {
		clean := strings.TrimSpace(alias)
		key := normalizeLookupKey(clean)
		if key == "" {
			continue
		}
		if _, ok := aliasSet[key]; ok {
			continue
		}
		aliasSet[key] = struct{}{}
		aliases = append(aliases, clean)
	}
	entry.Aliases = aliases
	return entry
}

func registerModelAlias(target map[string]string, alias string, id string) {
	key := normalizeLookupKey(alias)
	if key == "" || strings.TrimSpace(id) == "" {
		return
	}
	target[key] = strings.TrimSpace(id)
}

func sortedModelEntries(entries []ModelDefinition) []ModelDefinition {
	ordered := append([]ModelDefinition{}, entries...)
	rank := map[string]int{
		"auto":             0,
		"gpt-5.2":          1,
		"gpt-5.4":          2,
		"gemini-2.5-flash": 3,
		"gemini-3.1-pro":   4,
		"sonnet-4.6":       5,
		"opus-4.6":         6,
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		ri, okI := rank[ordered[i].ID]
		rj, okJ := rank[ordered[j].ID]
		switch {
		case okI && okJ:
			return ri < rj
		case okI:
			return true
		case okJ:
			return false
		default:
			return ordered[i].ID < ordered[j].ID
		}
	})
	return ordered
}

func normalizeLookupKey(value string) string {
	return slugModelID(value)
}

func slugModelID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "测试版", "")
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case r == '.' || r == '+':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	out = strings.ReplaceAll(out, "--", "-")
	return out
}
