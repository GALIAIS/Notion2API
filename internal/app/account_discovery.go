package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type discoveredAccountMetadata struct {
	Email         string
	UserID        string
	UserName      string
	SpaceID       string
	SpaceViewID   string
	SpaceName     string
	PlanType      string
	ClientVersion string
	Models        []ModelDefinition
}

type discoveredSpaceCandidate struct {
	ID        string
	ViewID    string
	Name      string
	PlanType  string
	AIEnabled bool
}

func unwrapRecordValue(raw any) map[string]any {
	node := mapValue(raw)
	if node == nil {
		return nil
	}
	if inner := mapValue(node["value"]); inner != nil {
		if nested := mapValue(inner["value"]); nested != nil {
			return nested
		}
		return inner
	}
	return node
}

func boolValue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func chooseBestSpace(recordMap map[string]any, userID string) discoveredSpaceCandidate {
	if recordMap == nil || strings.TrimSpace(userID) == "" {
		return discoveredSpaceCandidate{}
	}
	userRoots := mapValue(recordMap["user_root"])
	spaces := mapValue(recordMap["space"])
	if userRoots == nil || spaces == nil {
		return discoveredSpaceCandidate{}
	}
	root := unwrapRecordValue(userRoots[userID])
	if root == nil {
		return discoveredSpaceCandidate{}
	}
	pointers := sliceValue(root["space_view_pointers"])
	best := discoveredSpaceCandidate{}
	bestScore := -1
	for _, rawPointer := range pointers {
		pointer := mapValue(rawPointer)
		spaceID := strings.TrimSpace(stringValue(pointer["spaceId"]))
		if spaceID == "" {
			continue
		}
		value := unwrapRecordValue(spaces[spaceID])
		if value == nil {
			continue
		}
		settings := mapValue(value["settings"])
		disabledAI := boolValue(settings["disable_ai_feature"])
		enabledAI := boolValue(settings["enable_ai_feature"])
		candidate := discoveredSpaceCandidate{
			ID:        firstNonEmpty(strings.TrimSpace(stringValue(value["id"])), spaceID),
			ViewID:    strings.TrimSpace(stringValue(pointer["id"])),
			Name:      strings.TrimSpace(stringValue(value["name"])),
			PlanType:  strings.TrimSpace(stringValue(value["plan_type"])),
			AIEnabled: enabledAI || !disabledAI,
		}
		score := 0
		if candidate.AIEnabled {
			score += 2
		}
		if plan := strings.ToLower(candidate.PlanType); plan != "" && plan != "free" {
			score++
		}
		if candidate.Name != "" {
			score++
		}
		if score > bestScore {
			best = candidate
			bestScore = score
		}
	}
	return best
}

func parseLoadUserContentMetadata(payload map[string]any) discoveredAccountMetadata {
	recordMap := mapValue(payload["recordMap"])
	if recordMap == nil {
		return discoveredAccountMetadata{}
	}
	users := mapValue(recordMap["notion_user"])
	var meta discoveredAccountMetadata
	for userID, rawUser := range users {
		value := unwrapRecordValue(rawUser)
		if value == nil {
			continue
		}
		meta.UserID = strings.TrimSpace(userID)
		meta.Email = strings.TrimSpace(stringValue(value["email"]))
		meta.UserName = strings.TrimSpace(stringValue(value["name"]))
		break
	}
	space := chooseBestSpace(recordMap, meta.UserID)
	meta.SpaceID = space.ID
	meta.SpaceViewID = space.ViewID
	meta.SpaceName = space.Name
	meta.PlanType = space.PlanType
	return meta
}

func fetchLoadUserContentMetadata(ctx context.Context, client *http.Client, upstream NotionUpstream, clientVersion string, activeUserID string) (discoveredAccountMetadata, error) {
	payload, err := postNotionLoginJSON(ctx, client, upstream, upstream.API("loadUserContent"), clientVersion, upstream.HomeURL(), activeUserID, map[string]any{})
	if err != nil {
		return discoveredAccountMetadata{}, err
	}
	meta := parseLoadUserContentMetadata(payload)
	if meta.UserID == "" && meta.Email == "" && meta.SpaceID == "" {
		return discoveredAccountMetadata{}, fmt.Errorf("loadUserContent returned no account metadata")
	}
	return meta, nil
}

func fetchAvailableModelsMetadata(ctx context.Context, client *http.Client, upstream NotionUpstream, clientVersion string, activeUserID string) ([]ModelDefinition, error) {
	payload, err := postNotionLoginJSON(ctx, client, upstream, upstream.API("getAvailableModels"), clientVersion, upstream.HomeURL(), activeUserID, map[string]any{})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return parseProbeModelsBlob(string(raw)), nil
}

func discoverImportedAccountMetadata(ctx context.Context, cfg AppConfig, accountEmail string, cookies []ProbeCookie, fallback discoveredAccountMetadata) (discoveredAccountMetadata, error) {
	meta := fallback
	cookies = normalizeProbeCookies(cookies)
	if len(cookies) == 0 {
		return meta, fmt.Errorf("cookies are required for auto-discovery")
	}
	upstream := cfg.NotionUpstream()
	resolver := NewProxyResolver(cfg)
	client, err := newNotionLoginHTTPClient(helperTimeout(cfg), upstream, resolver, accountEmail)
	if err != nil {
		return meta, err
	}
	restoreProbeCookies(client.Jar, upstream.HomeURL(), cookies)
	restoreProbeCookies(client.Jar, upstream.LoginURL(), cookies)

	if strings.TrimSpace(meta.ClientVersion) == "" {
		bootstrap, err := fetchLoginBootstrap(ctx, client, upstream)
		if err != nil {
			return meta, err
		}
		meta.ClientVersion = strings.TrimSpace(bootstrap.ClientVersion)
	}

	lookupUserID := firstNonEmpty(
		meta.UserID,
		probeCookieValue(cookies, "notion_user_id"),
		probeCookieValue(probeCookiesFromJar(client.Jar, upstream.HomeURL()), "notion_user_id"),
		probeCookieValue(probeCookiesFromJar(client.Jar, upstream.LoginURL()), "notion_user_id"),
	)

	var primaryErr error
	if strings.TrimSpace(meta.ClientVersion) != "" {
		discovered, err := fetchLoadUserContentMetadata(ctx, client, upstream, meta.ClientVersion, lookupUserID)
		if err == nil {
			meta.Email = firstNonEmpty(meta.Email, discovered.Email)
			meta.UserID = firstNonEmpty(meta.UserID, discovered.UserID)
			meta.UserName = firstNonEmpty(meta.UserName, discovered.UserName)
			meta.SpaceID = firstNonEmpty(meta.SpaceID, discovered.SpaceID)
			meta.SpaceViewID = firstNonEmpty(meta.SpaceViewID, discovered.SpaceViewID)
			meta.SpaceName = firstNonEmpty(meta.SpaceName, discovered.SpaceName)
			meta.PlanType = firstNonEmpty(meta.PlanType, discovered.PlanType)
			lookupUserID = firstNonEmpty(meta.UserID, lookupUserID)
		} else {
			primaryErr = err
		}
	}

	if strings.TrimSpace(meta.UserID) == "" || strings.TrimSpace(meta.SpaceID) == "" || strings.TrimSpace(meta.Email) == "" {
		if strings.TrimSpace(meta.ClientVersion) != "" && strings.TrimSpace(lookupUserID) != "" {
			bootstrap, err := getSpacesInitial(ctx, client, upstream, meta.ClientVersion, lookupUserID)
			if err == nil {
				meta.UserID = firstNonEmpty(meta.UserID, lookupUserID)
				meta.Email = firstNonEmpty(meta.Email, bootstrap.Email)
				meta.UserName = firstNonEmpty(meta.UserName, bootstrap.UserName)
				meta.SpaceID = firstNonEmpty(meta.SpaceID, bootstrap.SpaceID)
				meta.SpaceViewID = firstNonEmpty(meta.SpaceViewID, bootstrap.SpaceViewID)
			} else if primaryErr == nil {
				primaryErr = err
			}
		}
	}

	if strings.TrimSpace(meta.ClientVersion) != "" {
		if models, err := fetchAvailableModelsMetadata(ctx, client, upstream, meta.ClientVersion, firstNonEmpty(meta.UserID, lookupUserID)); err == nil {
			meta.Models = models
		}
	}

	if strings.TrimSpace(meta.Email) == "" || strings.TrimSpace(meta.UserID) == "" || strings.TrimSpace(meta.SpaceID) == "" || strings.TrimSpace(meta.ClientVersion) == "" {
		if primaryErr != nil {
			return meta, fmt.Errorf("auto-discovery incomplete: %w", primaryErr)
		}
		return meta, fmt.Errorf("auto-discovery incomplete: missing email, user_id, space_id, or client_version")
	}
	return meta, nil
}
