package catalog

import (
	"fmt"
	"strings"

	"github.com/subosito/cincai/internal/catalog/fields"
)

var wireAliases = map[string]string{
	"xai-responses": "openai-responses",
}

var protocolAliases = map[string]string{
	"xai-responses": "openai-responses",
}

func normalizeProviders(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for name, raw := range in {
		entry, ok := raw.(map[string]any)
		if !ok {
			out[name] = raw
			continue
		}
		out[name] = normalizeProviderEntry(entry)
	}
	return out
}

// normalizeGroups keeps groups.<id>.models as ordered string lists (and optional description).
func normalizeGroups(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for name, raw := range in {
		entry, ok := raw.(map[string]any)
		if !ok {
			out[name] = raw
			continue
		}
		g := map[string]any{}
		if desc := fields.String(entry["description"]); desc != "" {
			g["description"] = desc
		}
		if hops, ok := entry["models"].([]any); ok {
			members := make([]any, 0, len(hops))
			for _, item := range hops {
				switch v := item.(type) {
				case string:
					if s := strings.TrimSpace(v); s != "" {
						members = append(members, s)
					}
				default:
					if s := fields.String(v); s != "" {
						members = append(members, s)
					}
				}
			}
			g["models"] = members
		}
		out[name] = g
	}
	return out
}

func normalizeProviderEntry(entry map[string]any) map[string]any {
	if out, ok := normalizeCapabilitiesProvider(entry); ok {
		return out
	}
	return entry
}

// normalizeCapabilitiesProvider expands one provider id into multi-surface routing.
// capabilities: chat, anthropic_chat, embed, image_gen|video_gen|speech_gen (generate).
func normalizeCapabilitiesProvider(entry map[string]any) (map[string]any, bool) {
	caps, ok := entry["capabilities"].(map[string]any)
	if !ok || len(caps) == 0 {
		return nil, false
	}
	out := map[string]any{}
	if cp := fields.String(entry["credential_profile"]); cp != "" {
		out["credential_profile"] = cp
	}
	// Optional per-provider HTTP(S) proxy (e.g. antigravity → gost).
	if proxy := fields.String(entry["proxy"]); proxy != "" {
		out["proxy"] = proxy
	}
	if allow, ok := entry["allow_models"].([]any); ok && len(allow) > 0 {
		list := make([]any, 0, len(allow))
		for _, item := range allow {
			if s := fields.String(item); s != "" {
				list = append(list, s)
			}
		}
		if len(list) > 0 {
			out["allow_models"] = list
		}
	}
	CopyInjectFields(entry, out)
	surfaces := make(map[string]any, len(caps))
	for capName, raw := range caps {
		capEntry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		surf := map[string]any{}
		if adapter := fields.String(capEntry["adapter"]); adapter != "" {
			surf["adapter"] = adapter
		}
		if protocol := aliasProtocol(fields.String(capEntry["protocol"])); protocol != "" {
			surf["protocol"] = protocol
		}
		if preset := fields.String(capEntry["request_preset"]); preset != "" {
			surf["request_preset"] = preset
		}
		if base := fields.FirstNonEmpty(fields.String(capEntry["base_url"]), fields.String(capEntry["url"])); base != "" {
			surf["base_url"] = base
		}
		if p := fields.String(capEntry["path"]); p != "" {
			surf["path"] = p
		}
		surfaces[capabilitySurfaceKey(capName)] = surf
	}
	if len(surfaces) == 0 {
		return nil, false
	}
	out["surfaces"] = surfaces
	return out, true
}

func capabilitySurfaceKey(cap string) string {
	return strings.TrimSpace(cap)
}

func normalizeModels(in map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for name, raw := range in {
		spec, ok := raw.(map[string]any)
		if !ok {
			out[name] = raw
			continue
		}
		norm, err := normalizeModelSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("models.%s: %w", name, err)
		}
		out[name] = norm
	}
	return out, nil
}

// removedModalityHint returns operator guidance for modality keys that used to
// exist and were removed. Returning an error beats the silent `continue` used
// for unknown keys: a deployed catalog carrying `search_web:` would otherwise
// load with the modality quietly gone, and the operator would only discover it
// from a missing model id. Loud at load time is the kinder failure.
func removedModalityHint(name string) string {
	switch strings.TrimSpace(name) {
	case "search_web", "search_x":
		return "modality removed: this was a routing alias and never enabled " +
			"search. Provider-executed search is enabled by the client declaring " +
			"the provider's search tool in the request body on the bare model id " +
			`(e.g. Responses tools:[{"type":"web_search"}]). Delete this key.`
	default:
		return ""
	}
}

func normalizeModelSpec(spec map[string]any) (map[string]any, error) {
	mods, ok := spec["modalities"].(map[string]any)
	if !ok {
		return spec, nil
	}
	outMods := make(map[string]any, len(mods))
	for modName, raw := range mods {
		route, ok := raw.(map[string]any)
		if !ok {
			outMods[modName] = raw
			continue
		}
		if hint := removedModalityHint(modName); hint != "" {
			return nil, fmt.Errorf("modalities.%s: %s", modName, hint)
		}
		target := cincaiModality(modName)
		if target == "" {
			continue
		}
		outMods[target] = normalizeModalityRoute(route, modName, target)
	}
	out := map[string]any{"modalities": outMods}
	// Preserve model-level effort metadata (not modality-scoped).
	for _, k := range []string{"efforts", "default_effort", "native_tools"} {
		if v, ok := spec[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func cincaiModality(name string) string {
	switch strings.TrimSpace(name) {
	case "chat":
		return "chat"
	// openai-responses ingress wire (translated to chat/anthropic by
	// wire-translate r2o/r2a). Stays a distinct modality key so a model
	// can carry both chat and responses routes on the same provider.
	case "responses":
		return "responses"
	// Second chat path on a different wire (e.g. openai-chat-completions
	// alongside openai-responses for the same model id). Stays nested on the
	// bare id because expand only facets same-wire collisions.
	case "chat_completions":
		return "chat_completions"
	case "embed":
		return "embed"
	case "image":
		return "image"
	case "image_gen":
		return "image_gen"
	case "video":
		return "video"
	case "video_gen":
		return "video_gen"
	case "voice":
		return "voice"
	case "speech_gen":
		return "speech_gen"
	// No search modality: provider-executed search is a tool the client
	// declares in the request body on the bare model id, not a route.
	// removedModalityHint rejects the old search_web / search_x keys.
	case "anthropic_chat":
		return "anthropic_chat"
	case "ocr":
		return "ocr"
	default:
		return ""
	}
}

func normalizeModalityRoute(route map[string]any, yamlKey, modality string) map[string]any {
	out := map[string]any{}
	if strat := fields.String(route["strategy"]); strat != "" {
		out["strategy"] = strat
	}
	if wire := normalizeWire(fields.String(route["wire"]), modality); wire != "" {
		out["wire"] = wire
	} else if w := defaultWire(yamlKey); w != "" {
		out["wire"] = w
	}
	// Composite model: ordered public model ids (xor with providers).
	// A map here, or a list of {model, effort} objects, is per-hop SKUs
	// (cursor: high → kimi-k3-high), not a composite hop list.
	if hops, ok := route["models"].([]any); ok && len(hops) > 0 {
		if skus := hopEffortModels(hops); len(skus) == 0 {
			outHops := make([]any, 0, len(hops))
			for _, item := range hops {
				switch v := item.(type) {
				case string:
					if s := strings.TrimSpace(v); s != "" {
						outHops = append(outHops, s)
					}
				default:
					if s := fields.String(v); s != "" {
						outHops = append(outHops, s)
					}
				}
			}
			out["models"] = outHops
			return out
		}
	}
	if ref := fields.String(route["provider_ref"]); ref != "" {
		entry := map[string]any{"provider_ref": ref}
		if model := fields.String(route["model"]); model != "" {
			entry["model"] = model
		}
		if surface := fields.String(route["surface"]); surface != "" {
			entry["surface"] = surface
		} else if surface := poolSurface(modality); surface != "" {
			entry["surface"] = surface
		}
		if adapter := fields.String(route["adapter"]); adapter != "" {
			entry["adapter"] = adapter
		}
		if skus := hopEffortModels(route["models"]); len(skus) > 0 {
			entry["models"] = skus
		}
		out["providers"] = []any{normalizePoolEntry(entry)}
		return out
	}
	if pool, ok := route["providers"].([]any); ok {
		entries := make([]any, 0, len(pool))
		for _, item := range pool {
			entry := normalizePoolEntry(item)
			if _, ok := entry["surface"]; !ok {
				if surface := poolSurface(modality); surface != "" {
					entry["surface"] = surface
				}
			}
			entries = append(entries, entry)
		}
		out["providers"] = entries
	}
	return out
}

func normalizePoolEntry(item any) map[string]any {
	switch v := item.(type) {
	case string:
		return map[string]any{"provider_ref": strings.TrimSpace(v)}
	case map[string]any:
		out := map[string]any{}
		if ref := fields.String(v["provider_ref"]); ref != "" {
			out["provider_ref"] = ref
		}
		if model := fields.String(v["model"]); model != "" {
			out["model"] = model
		}
		if surface := fields.String(v["surface"]); surface != "" {
			out["surface"] = surface
		}
		if adapter := fields.String(v["adapter"]); adapter != "" {
			out["adapter"] = adapter
		}
		if skus := hopEffortModels(v["models"]); len(skus) > 0 {
			out["models"] = skus
		}
		return out
	default:
		return map[string]any{}
	}
}

func hopEffortModels(raw any) map[string]any {
	out := map[string]any{}
	switch m := raw.(type) {
	case []any:
		for _, item := range m {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil
			}
			effort := strings.ToLower(strings.TrimSpace(fields.String(entry["effort"])))
			sku := strings.TrimSpace(fields.String(entry["model"]))
			if effort == "" || sku == "" {
				return nil
			}
			out[effort] = sku
		}
	case map[string]any:
		for k, v := range m {
			key := strings.ToLower(strings.TrimSpace(k))
			sku := strings.TrimSpace(fields.String(v))
			if key == "" || sku == "" {
				continue
			}
			out[key] = sku
		}
	case map[string]string:
		for k, v := range m {
			key := strings.ToLower(strings.TrimSpace(k))
			sku := strings.TrimSpace(v)
			if key == "" || sku == "" {
				continue
			}
			out[key] = sku
		}
	default:
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeWire(wire, _ string) string {
	return aliasProtocol(strings.TrimSpace(wire))
}

func aliasProtocol(protocol string) string {
	protocol = strings.TrimSpace(protocol)
	if protocol == "" {
		return ""
	}
	if alias, ok := protocolAliases[protocol]; ok {
		return alias
	}
	if alias, ok := wireAliases[protocol]; ok {
		return alias
	}
	return protocol
}

func poolSurface(modality string) string {
	switch modality {
	case "chat", "chat_completions":
		return "chat"
	case "anthropic_chat":
		return "anthropic_chat"
	case "embed":
		return "embed"
	case "image_gen":
		return "image_gen"
	case "video_gen":
		return "video_gen"
	case "speech_gen":
		return "speech_gen"
	case "voice":
		return "voice"
	case "ocr":
		return "ocr"
	default:
		return ""
	}
}

func defaultWire(yamlKey string) string {
	switch strings.TrimSpace(yamlKey) {
	case "voice":
		return "openai-audio-transcriptions"
	case "chat", "anthropic_chat", "image", "video":
		return "openai-chat-completions"
	case "embed":
		return "openai-embeddings"
	case "image_gen":
		return "openai-images-generations"
	case "video_gen":
		return "openai-videos"
	case "speech_gen":
		return "openai-audio-speech"
	case "ocr":
		return "openai-chat-completions"
	default:
		return ""
	}
}
