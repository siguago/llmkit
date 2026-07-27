// Package llmkit is a single-dependency-free Go SDK for talking to every major
// LLM vendor through one OpenAI-shaped interface.
//
// # Quick start
//
//	client, err := llmkit.New(llmkit.DeepSeek, llmkit.WithAPIKey(key))
//	if err != nil {
//		return err
//	}
//	text, err := client.Say(ctx, "deepseek-chat", "Explain CAP theorem in one paragraph.")
//
// The full surface is OpenAI-compatible, so anything you already know about
// chat completions applies:
//
//	resp, err := client.Chat(ctx, &llmkit.ChatRequest{
//		Model:    "deepseek-chat",
//		Messages: []llmkit.Message{llmkit.User("Hello")},
//	})
//
// # Provider differences
//
// Adapters translate the unified request into each vendor's native wire format
// (Anthropic Messages, Gemini generateContent, and the various OpenAI-compatible
// dialects). Vendor-specific knobs live as optional fields on ChatRequest;
// providers that don't understand a field ignore it, so the same request struct
// works everywhere and gains fidelity where the vendor supports it.
//
// Capabilities beyond chat are optional per provider, and they are tracked per
// endpoint rather than per feature — several vendors generate images without
// being able to edit them, and only one can cancel a video job. Ask before
// calling, or handle ErrUnsupported:
//
//	if client.SupportsImageGeneration() { ... }
//
// # Credentials
//
// New reads the provider's conventional environment variable when WithAPIKey is
// not supplied — OPENAI_API_KEY, ANTHROPIC_API_KEY, DEEPSEEK_API_KEY, and so on
// (see EnvVar).
//
// # Checking what a provider actually supports
//
// Capabilities vary by vendor and by model, and a vendor's docs don't always
// match what your account can reach. The bundled CLI probes it directly:
//
//	go run github.com/siguago/llmkit/cmd/llmkit-probe@latest deepseek
//
// It reports chat, streaming, multi-turn, tool calling, structured output,
// reasoning, vision, embeddings and error classification as PASS / FAIL / N/A,
// using your own key. In code, ask before calling:
//
//	if client.SupportsEmbeddings() { ... }
//
// or just call and handle ErrUnsupported.
package llmkit

import (
	"fmt"
	"os"
	"sort"

	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/anthropic"
	"github.com/siguago/llmkit/provider/cerebras"
	"github.com/siguago/llmkit/provider/dashscope"
	"github.com/siguago/llmkit/provider/deepseek"
	"github.com/siguago/llmkit/provider/easyrouter"
	"github.com/siguago/llmkit/provider/fireworks"
	"github.com/siguago/llmkit/provider/gemini"
	"github.com/siguago/llmkit/provider/groq"
	"github.com/siguago/llmkit/provider/minimax"
	"github.com/siguago/llmkit/provider/mistral"
	"github.com/siguago/llmkit/provider/moonshot"
	"github.com/siguago/llmkit/provider/ollama"
	"github.com/siguago/llmkit/provider/openai"
	"github.com/siguago/llmkit/provider/openrouter"
	"github.com/siguago/llmkit/provider/siliconflow"
	"github.com/siguago/llmkit/provider/together"
	"github.com/siguago/llmkit/provider/vercel"
	"github.com/siguago/llmkit/provider/vllm"
	"github.com/siguago/llmkit/provider/volcengine"
	"github.com/siguago/llmkit/provider/xai"
	"github.com/siguago/llmkit/provider/zhipu"
)

// Provider identifiers accepted by New.
const (
	OpenAI      = "openai"      // api.openai.com
	Anthropic   = "anthropic"   // api.anthropic.com (Claude)
	Gemini      = "gemini"      // generativelanguage.googleapis.com
	XAI         = "xai"         // api.x.ai (Grok)
	Mistral     = "mistral"     // api.mistral.ai
	DeepSeek    = "deepseek"    // api.deepseek.com
	Moonshot    = "moonshot"    // api.moonshot.ai (Kimi)
	Zhipu       = "zhipu"       // open.bigmodel.cn (GLM)
	MiniMax     = "minimax"     // api.minimax.io
	SiliconFlow = "siliconflow" // api.siliconflow.cn
	DashScope   = "dashscope"   // dashscope.aliyuncs.com (Qwen)
	Volcengine  = "volcengine"  // ark.cn-beijing.volces.com (Doubao)
	Groq        = "groq"        // api.groq.com — hosted open-weight inference
	Together    = "together"    // api.together.xyz — hosted open-weight inference
	Fireworks   = "fireworks"   // api.fireworks.ai — hosted open-weight inference
	Cerebras    = "cerebras"    // api.cerebras.ai — hosted open-weight inference
	Ollama      = "ollama"      // localhost:11434 — local runtime
	VLLM        = "vllm"        // self-hosted vLLM server
	OpenRouter  = "openrouter"  // openrouter.ai — multi-vendor aggregator
	EasyRouter  = "easyrouter"  // easyrouter.io — multi-vendor aggregator
	Vercel      = "vercel"      // ai-gateway.vercel.sh — multi-vendor aggregator
)

// Kimi is an alias for Moonshot, the vendor's consumer-facing brand.
const Kimi = Moonshot

// factories maps a provider name to its constructor. Every adapter takes the
// same (baseURL string) shape here; an empty baseURL selects the vendor's
// official endpoint.
var factories = map[string]func(baseURL string) provider.Provider{
	OpenAI:      func(b string) provider.Provider { return openai.NewWithBaseURL(b) },
	Anthropic:   func(b string) provider.Provider { return anthropic.NewWithBaseURL(b) },
	Gemini:      func(b string) provider.Provider { return gemini.NewWithBaseURL(b) },
	XAI:         func(b string) provider.Provider { return xai.New(b) },
	Mistral:     func(b string) provider.Provider { return mistral.New(b) },
	OpenRouter:  func(b string) provider.Provider { return openrouter.NewWithBaseURL(b) },
	EasyRouter:  func(b string) provider.Provider { return easyrouter.New(b) },
	Vercel:      func(b string) provider.Provider { return vercel.New(b) },
	Volcengine:  func(b string) provider.Provider { return volcengine.New(b) },
	DashScope:   func(b string) provider.Provider { return dashscope.New(b) },
	SiliconFlow: func(b string) provider.Provider { return siliconflow.New(b) },
	DeepSeek:    func(b string) provider.Provider { return deepseek.New(b) },
	Moonshot:    func(b string) provider.Provider { return moonshot.New(b) },
	Zhipu:       func(b string) provider.Provider { return zhipu.New(b) },
	MiniMax:     func(b string) provider.Provider { return minimax.New(b) },
	Groq:        func(b string) provider.Provider { return groq.New(b) },
	Together:    func(b string) provider.Provider { return together.New(b) },
	Fireworks:   func(b string) provider.Provider { return fireworks.New(b) },
	Cerebras:    func(b string) provider.Provider { return cerebras.New(b) },
	Ollama:      func(b string) provider.Provider { return ollama.New(b) },
	VLLM:        func(b string) provider.Provider { return vllm.New(b) },
}

// keyOptional lists providers that serve unauthenticated by default: runtimes
// you host yourself, reached over your own network. New builds these without a
// credential, and sends none unless you supply one (a vLLM server started with
// --api-key, an Ollama behind an authenticating proxy).
//
// Every other provider still fails closed with ErrNoAPIKey — for a real vendor,
// a missing credential is a configuration bug, not a mode.
var keyOptional = map[string]bool{
	Ollama: true,
	VLLM:   true,
}

// envVars maps a provider to the environment variable New falls back to.
var envVars = map[string]string{
	OpenAI:      "OPENAI_API_KEY",
	Anthropic:   "ANTHROPIC_API_KEY",
	Gemini:      "GEMINI_API_KEY",
	XAI:         "XAI_API_KEY",
	Mistral:     "MISTRAL_API_KEY",
	DeepSeek:    "DEEPSEEK_API_KEY",
	Moonshot:    "MOONSHOT_API_KEY",
	Zhipu:       "ZHIPU_API_KEY",
	MiniMax:     "MINIMAX_API_KEY",
	SiliconFlow: "SILICONFLOW_API_KEY",
	DashScope:   "DASHSCOPE_API_KEY",
	Volcengine:  "VOLCENGINE_API_KEY",
	Groq:        "GROQ_API_KEY",
	Together:    "TOGETHER_API_KEY",
	Fireworks:   "FIREWORKS_API_KEY",
	Cerebras:    "CEREBRAS_API_KEY",
	// Read when set, but never required — see keyOptional.
	Ollama:     "OLLAMA_API_KEY",
	VLLM:       "VLLM_API_KEY",
	OpenRouter: "OPENROUTER_API_KEY",
	EasyRouter: "EASYROUTER_API_KEY",
	Vercel:     "VERCEL_AI_GATEWAY_KEY",
}

// EnvVar returns the environment variable New reads for the given provider when
// WithAPIKey is not supplied, or "" for an unknown provider.
func EnvVar(providerName string) string { return envVars[providerName] }

// KeyOptional reports whether the named provider constructs without a
// credential — see keyOptional for which ones and why.
//
// It exists so that tooling around this package doesn't have to hardcode the
// list. Anything that gates on "is a key configured for this provider" needs it,
// or it will refuse to talk to a local runtime that never had a key to
// configure: llmkit-probe's target selection is exactly that case.
func KeyOptional(providerName string) bool { return keyOptional[providerName] }

// Providers lists every supported provider name, sorted.
func Providers() []string {
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// New builds a Client for the named provider.
//
// The credential comes from WithAPIKey, or failing that from the provider's
// conventional environment variable (see EnvVar). New returns ErrNoAPIKey when
// neither is present, and ErrUnknownProvider for an unrecognized name.
//
// Two things waive the credential requirement: a provider that serves
// unauthenticated by default (see KeyOptional), and WithoutAPIKey for anything
// else. In both cases no credential header is sent at all.
//
// Retries default to DefaultRetry(); pass WithRetry(NoRetry()) to opt out.
func New(providerName string, opts ...Option) (*Client, error) {
	factory, ok := factories[providerName]
	if !ok {
		return nil, fmt.Errorf("%w: %q (known: %v)", ErrUnknownProvider, providerName, Providers())
	}

	cfg := clientConfig{retry: DefaultRetry()}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.apiKey == "" {
		if env := envVars[providerName]; env != "" {
			cfg.apiKey = os.Getenv(env)
		}
	}
	if cfg.apiKey == "" && !cfg.noAPIKey && !keyOptional[providerName] {
		return nil, fmt.Errorf("%w: provider %q (env %s)", ErrNoAPIKey, providerName, envVars[providerName])
	}

	return &Client{
		name:     providerName,
		provider: factory(cfg.baseURL),
		cfg:      cfg,
	}, nil
}

// Wrap builds a Client around an adapter you constructed yourself. Use it for a
// provider configured in a way the factory doesn't cover, or for a custom
// implementation of provider.Provider (a fake in tests, an internal gateway).
//
// The credential rules are New's: WithAPIKey, then the environment variable for
// p.Name() if there is one, then ErrNoAPIKey — unless KeyOptional(p.Name()) or
// WithoutAPIKey says the endpoint needs none.
func Wrap(p provider.Provider, opts ...Option) (*Client, error) {
	if p == nil {
		return nil, fmt.Errorf("llmkit: nil provider")
	}
	cfg := clientConfig{retry: DefaultRetry()}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.apiKey == "" {
		if env := envVars[p.Name()]; env != "" {
			cfg.apiKey = os.Getenv(env)
		}
	}
	if cfg.apiKey == "" && !cfg.noAPIKey && !keyOptional[p.Name()] {
		return nil, fmt.Errorf("%w: provider %q", ErrNoAPIKey, p.Name())
	}
	return &Client{name: p.Name(), provider: p, cfg: cfg}, nil
}
