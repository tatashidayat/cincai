// Package catalog resolves multi-surface providers.yaml to upstream targets.
package catalog

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Wire IDs.
const (
	WireOpenAIChat                = "openai-chat-completions"
	WireAnthropicMsg              = "anthropic-messages"
	WireOpenAIEmbed               = "openai-embeddings"
	WireOpenAIResponses           = "openai-responses"
	WireOpenAIImagesGen           = "openai-images-generations"
	WireOpenAIAudioSpeech         = "openai-audio-speech"
	WireOpenAIAudioTranscriptions = "openai-audio-transcriptions"
	WireOpenAIVideos              = "openai-videos"
)

// Strategy names.
const (
	StrategyFailover   = "failover"
	StrategyRoundRobin = "round_robin"
)

// Surface is one upstream endpoint on a provider (passthrough protocol or translate adapter).
type Surface struct {
	Protocol string `yaml:"protocol,omitempty"`
	Adapter  string `yaml:"adapter,omitempty"`
	BaseURL  string `yaml:"base_url"`
	// Path overrides the upstream request path for passthrough protocols.
	// Use it for hosts that deviate from the wire's default layout, e.g.
	// the ChatGPT Codex backend (base_url …/backend-api/codex, path /responses).
	// Empty = the protocol's default path (e.g. /v1/chat/completions).
	Path          string `yaml:"path,omitempty"`
	RequestPreset string `yaml:"request_preset,omitempty"`
}

// Provider is a logical vendor connection.
type Provider struct {
	CredentialProfile string `yaml:"credential_profile"`
	// Proxy is an optional HTTP(S) proxy URL for all upstream calls on this
	// provider (e.g. "http://127.0.0.1:8080"). Empty = use process default
	// (ProxyFromEnvironment). "direct" forces no proxy even if env is set.
	Proxy        string            `yaml:"proxy,omitempty"`
	Inject       map[string]string `yaml:"inject,omitempty"`
	InjectPreset string            `yaml:"inject_preset,omitempty"`
	// AllowModels restricts which upstream model ids may leave this provider.
	// Empty or omitted = allow all. Exact match on Target.UpstreamModel after effort rewrite.
	AllowModels []string           `yaml:"allow_models,omitempty"`
	Surfaces    map[string]Surface `yaml:"surfaces"`
}

// PoolEntry is one upstream in a model pool.
type PoolEntry struct {
	ProviderRef string `yaml:"provider_ref"`
	Model       string `yaml:"model"`
	Surface     string `yaml:"surface,omitempty"`
	// Adapter overrides the provider surface handler (e.g. dashscope-omni on surface: video).
	Adapter string `yaml:"adapter,omitempty"`
	// Models is an effort → SKU map for this hop (cursor: high → kimi-k3-high).
	// Authoring may be a map or a list of {model, effort}; load normalizes to a map.
	// Mutually exclusive with Model. Default SKU is models[default_effort].
	Models map[string]string `yaml:"models,omitempty"`
}

// Modality binds wire + pool.
//
// Exactly one of Providers or Models must be set:
//   - Providers: leaf model — ordered upstream provider pool
//   - Models: composite model — ordered public model ids (same catalog);
//     each hop is fully resolved (its own provider pool or nested models)
type Modality struct {
	Wire      string      `yaml:"wire"`
	Strategy  string      `yaml:"strategy,omitempty"`
	Providers []PoolEntry `yaml:"providers,omitempty"`
	// Models is an ordered list of public catalog model ids. When set, this
	// modality is a composite: resolve each hop and try them under Strategy
	// (typically failover). Mutually exclusive with Providers / single provider_ref.
	Models []string `yaml:"models,omitempty"`
}

// Model is a catalog id.
type Model struct {
	// Efforts lists allowed reasoning-effort levels (e.g. low, medium, high).
	// Empty means the model does not advertise effort controls. Clients should
	// use this list when present instead of a static none|low|medium|high set.
	// Per-hop SKUs are selected via PoolEntry.Effort (model + effort on the hop).
	// Untagged hops may still rewrite a pool-model tier suffix
	// (example-model-medium + high → example-model-high).
	Efforts []string `yaml:"efforts,omitempty"`
	// DefaultEffort is applied when the client omits effort. Must be in Efforts.
	DefaultEffort string `yaml:"default_effort,omitempty"`
	// NativeTools are provider-executed tools this model can run, authored as
	// the wire-shaped declarations a client sends (e.g. [{type: web_search}]).
	// Published on /v1/models so clients read capability from the catalog
	// instead of repeating it in their own config.
	NativeTools []map[string]any    `yaml:"native_tools,omitempty"`
	Modalities  map[string]Modality `yaml:"modalities"`
}

// SupportsNativeTool returns true if the model declares a native tool with the given type or name.
func (m Model) SupportsNativeTool(toolType string) bool {
	if toolType == "" {
		return false
	}
	for _, tool := range m.NativeTools {
		if t, ok := tool["type"].(string); ok && t == toolType {
			return true
		}
		if n, ok := tool["name"].(string); ok && n == toolType {
			return true
		}
	}
	return false
}

// ModelGroup is a named set of public model ids for client discovery (menus,
// pickers). Not callable: POST with model=<group id> is rejected. Listed on
// GET /v1/models with object "model_group".
type ModelGroup struct {
	// Description is optional operator/UI text.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Models is the ordered member list (callable catalog ids: leaf or composite).
	Models []string `yaml:"models" json:"models"`
}

// Document is providers.yaml root.
type Document struct {
	Providers map[string]Provider   `yaml:"providers"`
	Models    map[string]Model      `yaml:"models"`
	Groups    map[string]ModelGroup `yaml:"groups,omitempty"`
}

// Catalog is loaded operator config.
type Catalog struct {
	doc Document
	rr  map[string]*roundRobinState
	mu  sync.Mutex
	// loadedAt backs ModelListItem.Created. See listModels for why.
	loadedAt int64
	// meta is optional per-model context/pricing from models.meta.yaml (MergeMeta).
	meta map[string]ModelMeta
	// metaVersion is billing.version from the last merged meta document.
	metaVersion string
}

type roundRobinState struct {
	idx int
}

// Load reads providers.yaml.
func Load(path string) (*Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc Document
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return NewFromDocument(doc)
}

// NewFromDocument builds a catalog from an in-memory document (after shim normalization).
// Same-wire multi-modality rows are expanded to base:facet model ids (see ExpandWireCollisions).
func NewFromDocument(doc Document) (*Catalog, error) {
	if err := ExpandWireCollisions(&doc); err != nil {
		return nil, err
	}
	return &Catalog{doc: doc, rr: make(map[string]*roundRobinState), loadedAt: time.Now().Unix()}, nil
}

// Model returns the live model entry (after meta merge of efforts when applied).
func (c *Catalog) Model(id string) (Model, bool) {
	if c == nil {
		return Model{}, false
	}
	m, ok := c.doc.Models[id]
	return m, ok
}

// WireForPath maps HTTP path to wire id.
func WireForPath(path string) (string, bool) {
	switch strings.TrimSpace(path) {
	case "/v1/chat/completions":
		return WireOpenAIChat, true
	case "/v1/messages", "/v1/messages/count_tokens":
		return WireAnthropicMsg, true
	case "/v1/embeddings":
		return WireOpenAIEmbed, true
	case "/v1/responses":
		return WireOpenAIResponses, true
	case "/v1/images/generations", "/v1/images/edits":
		return WireOpenAIImagesGen, true
	case "/v1/audio/speech":
		return WireOpenAIAudioSpeech, true
	case "/v1/audio/transcriptions":
		return WireOpenAIAudioTranscriptions, true
	case "/v1/videos/generations", "/v1/videos/edits", "/v1/videos/extensions":
		return WireOpenAIVideos, true
	default:
		if strings.HasPrefix(path, "/v1/videos/") {
			return WireOpenAIVideos, true
		}
		return "", false
	}
}

// Target is resolved upstream route.
type Target struct {
	Model       string
	ProviderRef string
	Protocol    string
	Adapter     string
	BaseURL     string
	// Path is the per-surface upstream path override (Surface.Path); empty = protocol default.
	Path              string
	CredentialProfile string
	UpstreamModel     string
	// EffortModels is copied from the pool hop (effort → SKU). Empty = body-only / suffix.
	EffortModels map[string]string
	// Proxy is the provider-level HTTP(S) proxy URL (see Provider.Proxy).
	Proxy         string
	Inject        map[string]string
	InjectPreset  string
	RequestPreset string
	// AllowModels is copied from the provider; empty = no extra filter.
	AllowModels []string
}

// RoutePlan is the ordered upstream attempt list for one model + wire.
type RoutePlan struct {
	Strategy string
	Targets  []Target
	// Models is set when the requested id is a composite (modality.models hops).
	// Ordered public model ids as configured; empty for leaf models.
	Models []string
}

// Resolve picks upstream route(s) for model + wire.
// Failover returns all pool entries in yaml order; round_robin returns one entry.
func (c *Catalog) Resolve(model, wire string) (RoutePlan, error) {
	return c.ResolveWithModality(model, wire, "")
}

// ResolveWithModality picks upstream route(s) for model + wire, optionally constrained
// by catalog modality key (models.<id>.modalities.<key>). Empty modality auto-selects
// when unambiguous. Same-wire multi-modality should be expanded to base:facet ids at
// load (ExpandWireCollisions); residual collisions error with that guidance.
//
// When the modality sets models: [id, …], each hop is resolved (providers or nested
// models) and targets are concatenated under strategy failover (or one hop for
// round_robin). Cycles are rejected.
func (c *Catalog) ResolveWithModality(model, wire, modality string) (RoutePlan, error) {
	return c.resolveWithModality(model, wire, modality, nil)
}

func (c *Catalog) resolveWithModality(model, wire, modality string, stack []string) (RoutePlan, error) {
	for _, s := range stack {
		if s == model {
			return RoutePlan{}, fmt.Errorf("model %q: cycle in models chain (%s → %s)", model, strings.Join(stack, " → "), model)
		}
	}
	stack = append(stack, model)

	m, ok := c.doc.Models[model]
	if !ok {
		return RoutePlan{}, fmt.Errorf("unknown model %q", model)
	}
	var modNames []string
	for name, md := range m.Modalities {
		if md.Wire == wire {
			modNames = append(modNames, name)
		}
	}
	sort.Strings(modNames)
	if len(modNames) == 0 {
		return RoutePlan{}, fmt.Errorf("model %q has no modality for wire %q", model, wire)
	}
	modName, err := pickModality(modNames, modality)
	if err != nil {
		return RoutePlan{}, err
	}
	mod := m.Modalities[modName]
	hasProviders := len(mod.Providers) > 0
	hasModels := len(mod.Models) > 0
	if hasProviders && hasModels {
		return RoutePlan{}, fmt.Errorf("model %q modality %q: set providers or models, not both", model, modName)
	}
	if !hasProviders && !hasModels {
		return RoutePlan{}, fmt.Errorf("model %q: empty provider pool (and no models chain)", model)
	}

	strat := mod.Strategy
	if strat == "" {
		strat = StrategyFailover
	}
	if strat == "sticky" {
		return RoutePlan{}, fmt.Errorf("strategy %q removed; use %q or %q", strat, StrategyFailover, StrategyRoundRobin)
	}

	if hasModels {
		return c.resolveModelChain(model, wire, modName, mod, strat, stack)
	}

	poolKey := model + "|" + modName + "|" + wire
	entries, err := c.pickEntries(poolKey, strat, mod.Providers)
	if err != nil {
		return RoutePlan{}, err
	}
	targets := make([]Target, 0, len(entries))
	for _, entry := range entries {
		t, err := c.targetFromEntry(model, entry, wire)
		if err != nil {
			return RoutePlan{}, err
		}
		targets = append(targets, t)
	}
	return RoutePlan{Strategy: strat, Targets: targets}, nil
}

func (c *Catalog) resolveModelChain(model, wire, modName string, mod Modality, strat string, stack []string) (RoutePlan, error) {
	hops := make([]string, 0, len(mod.Models))
	for _, h := range mod.Models {
		h = strings.TrimSpace(h)
		if h == "" {
			return RoutePlan{}, fmt.Errorf("model %q modality %q: empty entry in models", model, modName)
		}
		hops = append(hops, h)
	}
	if len(hops) == 0 {
		return RoutePlan{}, fmt.Errorf("model %q modality %q: empty models list", model, modName)
	}

	// round_robin: pick one hop, then use that hop's full resolve (its strategy).
	if strat == StrategyRoundRobin {
		c.mu.Lock()
		poolKey := model + "|" + modName + "|" + wire + "|models"
		st := c.rr[poolKey]
		if st == nil {
			st = &roundRobinState{}
			c.rr[poolKey] = st
		}
		i := st.idx % len(hops)
		st.idx++
		c.mu.Unlock()
		hop := hops[i]
		hopPlan, err := c.resolveWithModality(hop, wire, "", stack)
		if err != nil {
			return RoutePlan{}, fmt.Errorf("model %q hop %q: %w", model, hop, err)
		}
		return RoutePlan{Strategy: hopPlan.Strategy, Targets: hopPlan.Targets, Models: hops}, nil
	}

	// failover (default): concatenate every hop's targets in order.
	var targets []Target
	for _, hop := range hops {
		hopPlan, err := c.resolveWithModality(hop, wire, "", stack)
		if err != nil {
			return RoutePlan{}, fmt.Errorf("model %q hop %q: %w", model, hop, err)
		}
		// Nested composite keeps its own strategy among its targets; we flatten
		// under parent failover so hop1's providers try fully before hop2.
		targets = append(targets, hopPlan.Targets...)
	}
	if len(targets) == 0 {
		return RoutePlan{}, fmt.Errorf("model %q: models chain resolved to no targets", model)
	}
	return RoutePlan{Strategy: StrategyFailover, Targets: targets, Models: hops}, nil
}

func pickModality(candidates []string, hint string) (string, error) {
	hint = strings.TrimSpace(hint)
	if hint != "" {
		for _, name := range candidates {
			if name == hint {
				return name, nil
			}
		}
		return "", fmt.Errorf("model modality %q not available for wire", hint)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	// A same-wire collision that survived expand is a config error: distinct
	// ids are required. There is no search special case — provider-executed
	// search is a client-declared tool on the bare model, not a modality.
	return "", fmt.Errorf("multiple modalities for wire: %s (use distinct model ids base%sfacet; catalog expand should have split these at load)", strings.Join(candidates, ", "), FacetSeparator)
}

func (c *Catalog) targetFromEntry(model string, entry PoolEntry, wire string) (Target, error) {
	prov, ok := c.doc.Providers[entry.ProviderRef]
	if !ok {
		return Target{}, fmt.Errorf("unknown provider_ref %q", entry.ProviderRef)
	}
	surf, err := c.surfaceFor(prov, entry, wire)
	if err != nil {
		return Target{}, err
	}
	upstreamModel := strings.TrimSpace(entry.Model)
	if len(entry.Models) > 0 {
		// models owns the SKU. Default is models[default_effort], else first efforts key.
		if m, ok := c.doc.Models[model]; ok {
			if sku, ok := lookupHopEffortModel(entry.Models, resolvedDefaultEffort(m)); ok {
				upstreamModel = sku
			}
		}
		if upstreamModel == "" {
			for _, sku := range entry.Models {
				if s := strings.TrimSpace(sku); s != "" {
					upstreamModel = s
					break
				}
			}
		}
	}
	if upstreamModel == "" {
		upstreamModel = strings.TrimSpace(model)
	}
	adapter := surf.Adapter
	if entry.Adapter != "" {
		adapter = entry.Adapter
	}
	return Target{
		Model: model, ProviderRef: entry.ProviderRef,
		Protocol: surf.Protocol, Adapter: adapter, BaseURL: surf.BaseURL,
		Path:              surf.Path,
		CredentialProfile: prov.CredentialProfile,
		UpstreamModel:     upstreamModel,
		EffortModels:      entry.Models,
		Proxy:             strings.TrimSpace(prov.Proxy),
		Inject:            prov.Inject,
		InjectPreset:      prov.InjectPreset,
		RequestPreset:     surf.RequestPreset,
		AllowModels:       prov.AllowModels,
	}, nil
}

func (c *Catalog) surfaceFor(prov Provider, entry PoolEntry, wire string) (Surface, error) {
	if entry.Surface != "" {
		s, ok := prov.Surfaces[entry.Surface]
		if !ok {
			return Surface{}, fmt.Errorf("surface %q not found on provider", entry.Surface)
		}
		return s, nil
	}
	ids := make([]string, 0, len(prov.Surfaces))
	for id := range prov.Surfaces {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := prov.Surfaces[id]
		if s.Adapter != "" {
			continue
		}
		if protocolMatchesWire(s.Protocol, wire) {
			return s, nil
		}
	}
	return Surface{}, fmt.Errorf("no surface for wire %q", wire)
}

func protocolMatchesWire(protocol, wire string) bool {
	// Core matches OpenAI + Anthropic ingress wires only. Vendor protocols
	// require an explicit pool surface in providers.yaml; translate adapters
	// use surface.adapter instead of vendor protocol names.
	switch wire {
	case WireOpenAIChat:
		return protocol == "openai-chat-completions" || protocol == "openai-compat-chat"
	case WireAnthropicMsg:
		return protocol == "anthropic-messages"
	case WireOpenAIEmbed:
		return protocol == "openai-embeddings"
	case WireOpenAIResponses:
		return protocol == "openai-responses"
	case WireOpenAIImagesGen:
		return protocol == "openai-images"
	case WireOpenAIAudioSpeech:
		return protocol == "openai-tts"
	case WireOpenAIAudioTranscriptions:
		return protocol == "openai-transcriptions"
	case WireOpenAIVideos:
		return protocol == "openai-videos"
	default:
		return false
	}
}

func (c *Catalog) pickEntries(poolKey, strat string, pool []PoolEntry) ([]PoolEntry, error) {
	if len(pool) == 0 {
		return nil, fmt.Errorf("empty provider pool")
	}
	switch strat {
	case StrategyFailover:
		return pool, nil
	case StrategyRoundRobin:
		c.mu.Lock()
		defer c.mu.Unlock()
		st := c.rr[poolKey]
		if st == nil {
			st = &roundRobinState{}
			c.rr[poolKey] = st
		}
		i := st.idx % len(pool)
		st.idx++
		return []PoolEntry{pool[i]}, nil
	default:
		return nil, fmt.Errorf("unknown strategy %q", strat)
	}
}

// RegisteredRoutes returns protocol and adapter names referenced in catalog.
func (c *Catalog) RegisteredRoutes() (protocols, adapters []string) {
	seenP := map[string]bool{}
	seenA := map[string]bool{}
	for _, p := range c.doc.Providers {
		for _, s := range p.Surfaces {
			if s.Adapter != "" {
				seenA[s.Adapter] = true
			}
			if s.Protocol != "" {
				seenP[s.Protocol] = true
			}
		}
	}
	for _, m := range c.doc.Models {
		for _, md := range m.Modalities {
			for _, entry := range md.Providers {
				if entry.Adapter != "" {
					seenA[entry.Adapter] = true
				}
			}
		}
	}
	for k := range seenP {
		protocols = append(protocols, k)
	}
	for k := range seenA {
		adapters = append(adapters, k)
	}
	return protocols, adapters
}

// Doctor checks catalog routes against registered handler sets.
func Doctor(cat *Catalog, registeredProtocols, registeredAdapters map[string]bool) []string {
	var missing []string
	protos, adpts := cat.RegisteredRoutes()
	for _, p := range protos {
		if !registeredProtocols[p] {
			missing = append(missing, "protocol:"+p)
		}
	}
	for _, a := range adpts {
		if !registeredAdapters[a] {
			missing = append(missing, "adapter:"+a)
		}
	}
	return missing
}
