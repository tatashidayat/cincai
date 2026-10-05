package passthrough

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/subosito/cincai/adaptersdk"
	"github.com/subosito/cincai/adaptersdk/handler"
	"github.com/subosito/cincai/catalog"
	"github.com/subosito/cincai/credential/inject"
	"github.com/subosito/cincai/observability"
	"github.com/subosito/cincai/upstream"
)

// Adapter provides passthrough relay handlers (chat + embeddings + media, no transformation).
type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string { return "passthrough" }

func (a *Adapter) Register(reg *adaptersdk.Registry) error {
	adaptersdk.RegisterChat(reg, &ChatHandler{ProtocolName: "openai-chat-completions", WireID: catalog.WireOpenAIChat})
	adaptersdk.RegisterChat(reg, &ChatHandler{ProtocolName: "anthropic-messages", WireID: catalog.WireAnthropicMsg})
	adaptersdk.RegisterChat(reg, &ChatHandler{ProtocolName: "openai-responses", WireID: catalog.WireOpenAIResponses, Path: "/v1/responses"})
	adaptersdk.RegisterEmbed(reg, &EmbedHandler{ProtocolName: "openai-embeddings", WireID: catalog.WireOpenAIEmbed})
	adaptersdk.RegisterImage(reg, &ImageHandler{ProtocolName: "openai-images", WireID: catalog.WireOpenAIImagesGen})
	adaptersdk.RegisterSpeech(reg, &SpeechHandler{ProtocolName: "openai-tts", WireID: catalog.WireOpenAIAudioSpeech})
	adaptersdk.RegisterTranscription(reg, &TranscriptionHandler{ProtocolName: "openai-transcriptions", WireID: catalog.WireOpenAIAudioTranscriptions})
	adaptersdk.RegisterVideo(reg, &VideoHandler{ProtocolName: "openai-videos", WireID: catalog.WireOpenAIVideos})
	return nil
}

// ChatHandler relays chat-shaped requests without transformation.
type ChatHandler struct {
	ProtocolName string
	WireID       string
	Path         string
}

func (h *ChatHandler) Protocol() string { return h.ProtocolName }

func (h *ChatHandler) Forward(ctx context.Context, client *http.Client, t handler.Target, body io.Reader, hdr http.Header) (*http.Response, error) {
	path := h.UpstreamPath()
	// Catalog path override wins (e.g. ChatGPT Codex /responses — no /v1 prefix).
	if p := strings.TrimSpace(t.Path); p != "" {
		path = p
	}
	return forward(ctx, client, t, path, body, hdr)
}

// UpstreamPath returns the upstream HTTP path for this handler.
func (h *ChatHandler) UpstreamPath() string {
	if h.Path != "" {
		return h.Path
	}
	if h.WireID == catalog.WireAnthropicMsg {
		return "/v1/messages"
	}
	return "/v1/chat/completions"
}

// EmbedHandler relays embedding requests without transformation.
type EmbedHandler struct {
	ProtocolName string
	WireID       string
}

func (h *EmbedHandler) Protocol() string { return h.ProtocolName }

func (h *EmbedHandler) Forward(ctx context.Context, client *http.Client, t handler.Target, body io.Reader, hdr http.Header) (*http.Response, error) {
	return forward(ctx, client, t, "/v1/embeddings", body, hdr)
}

func forward(ctx context.Context, client *http.Client, t handler.Target, path string, body io.Reader, hdr http.Header) (*http.Response, error) {
	return relay(ctx, client, t, http.MethodPost, path, body, hdr)
}

// RelayPath forwards POST to an explicit upstream path (e.g. /v1/messages/count_tokens).
func RelayPath(ctx context.Context, client *http.Client, t handler.Target, path string, body io.Reader, hdr http.Header) (*http.Response, error) {
	return relay(ctx, client, t, http.MethodPost, path, body, hdr)
}

func relay(ctx context.Context, client *http.Client, t handler.Target, method, path string, body io.Reader, hdr http.Header) (*http.Response, error) {
	url := path
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		// absolute upstream URL (image adapters)
	} else {
		url = upstream.JoinURL(t.BaseURL, path)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	inject.CopyHeaders(req, hdr)
	if err := inject.ApplyRoute(t.Material, req, inject.Route{Spec: t.Inject, Preset: t.InjectPreset}, inject.AdapterDefault{}); err != nil {
		return nil, err
	}
	return observability.HTTPDo(ctx, client, req)
}
