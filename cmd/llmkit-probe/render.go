package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/siguago/llmkit"
)

// Output deliberately avoids color: probe output gets pasted into issues and
// piped into files, where escape codes are noise. The four outcome markers are
// distinguishable in plain text.
const (
	markPass = "PASS"
	markFail = "FAIL"
	markNA   = " N/A"
	markSkip = "SKIP"
)

func mark(o outcome) string {
	switch o {
	case pass:
		return markPass
	case fail:
		return markFail
	case notApplicable:
		return markNA
	default:
		return markSkip
	}
}

func printHeader(r report) {
	fmt.Printf("llmkit probe · %s · %s\n", r.provider, r.model)
	fmt.Println(strings.Repeat("─", 78))
}

func printSetupError(err error) {
	fmt.Printf("  %s  初始化失败: %v\n\n", markFail, err)
}

// printResult renders one row as soon as its probe finishes, so a slow model
// never looks like a hang.
//
// Padding is by display width, not by len(): Go's %-18s counts bytes, and a CJK
// rune is 3 bytes wide but 2 columns wide, so a mixed-script table drifts badly
// with the stdlib verb.
func printResult(res result, verbose bool) {
	fmt.Printf("  %s  %s %s %8s\n",
		mark(res.outcome),
		padDisplay(res.name, 20),
		padDisplay(res.detail, 40),
		res.elapsed.Round(time.Millisecond),
	)
	if verbose && res.body != "" {
		for _, line := range strings.Split(strings.TrimSpace(res.body), "\n") {
			fmt.Printf("        │ %s\n", line)
		}
	}
}

// printPreflightFailure explains why no capability probes ran, with the fix.
func printPreflightFailure(providerName, model string, err error) {
	fmt.Printf("  %s  连通性检查未通过，已中止\n\n", markFail)
	fmt.Printf("        %s\n\n", describeErr(err))

	switch {
	case llmkit.IsAuthError(err):
		fmt.Printf("  key 无效或无权限。检查 %s 是否设置正确。\n", llmkit.EnvVar(providerName))
	case llmkit.IsNotFound(err):
		fmt.Printf("  模型 %q 不存在或未在该账号开通。换一个:\n", model)
		fmt.Printf("    llmkit-probe %s -model <其他模型>\n", providerName)
	case llmkit.IsRateLimited(err):
		fmt.Println("  当前被限流，稍后再试。")
	case llmkit.KeyOptional(providerName):
		// A local runtime that isn't listening is the overwhelmingly likely cause
		// here, and "检查 HTTPS_PROXY" would send the reader off in the wrong
		// direction — the default endpoint is loopback, which no proxy touches.
		fmt.Printf("  连不上本地运行时。确认它起着，端口对得上；不是默认端口就传 -base-url。\n")
		fmt.Printf("    llmkit-probe %s -base-url http://localhost:<端口>/v1 -model <你起的模型>\n", providerName)
	default:
		fmt.Println("  网络或上游异常。如在国内访问海外厂商，检查 HTTPS_PROXY 是否已设置。")
	}
	fmt.Println("\n  （未执行能力探测：连不上上游时的探测结果没有意义）")
	fmt.Println()
}

func printSummary(r report) {
	var passed, failed, na, skip int
	for _, res := range r.results {
		switch res.outcome {
		case pass:
			passed++
		case fail:
			failed++
		case notApplicable:
			na++
		default:
			skip++
		}
	}

	fmt.Println(strings.Repeat("─", 78))
	parts := []string{fmt.Sprintf("%d 通过", passed)}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d 失败", failed))
	}
	if na > 0 {
		parts = append(parts, fmt.Sprintf("%d 不适用", na))
	}
	if skip > 0 {
		parts = append(parts, fmt.Sprintf("%d 跳过", skip))
	}
	summary := strings.Join(parts, " · ")
	if r.tokens > 0 {
		summary += fmt.Sprintf("  ·  约 %d tokens", r.tokens)
	}
	fmt.Printf("  %s\n", summary)

	if failed > 0 {
		fmt.Println()
		fmt.Println("  失败项:")
		for _, res := range r.results {
			if res.outcome == fail {
				fmt.Printf("    · %s — %s\n", res.name, res.detail)
			}
		}
		fmt.Println()
		fmt.Println("  提示: 失败可能来自模型本身而非 SDK。换个模型试试:")
		fmt.Printf("    llmkit-probe %s -model <其他模型> -v\n", r.provider)
	}
	fmt.Println()
}

func printGrandTotal(reports []report) {
	fmt.Println(strings.Repeat("═", 78))
	fmt.Println("汇总")
	fmt.Println(strings.Repeat("─", 78))
	fmt.Printf("  %-13s %-26s %6s %6s %6s %10s\n", "PROVIDER", "MODEL", "通过", "失败", "不适用", "TOKENS")

	var totalTokens int
	for _, r := range reports {
		var passed, failed, na int
		for _, res := range r.results {
			switch res.outcome {
			case pass:
				passed++
			case fail:
				failed++
			case notApplicable:
				na++
			}
		}
		totalTokens += r.tokens
		model := truncateDisplay(r.model, 26)
		if r.setupErr != nil {
			fmt.Printf("  %-13s %-26s %s\n", r.provider, model, "初始化失败")
			continue
		}
		fmt.Printf("  %-13s %-26s %6d %6d %6d %10d\n", r.provider, model, passed, failed, na, r.tokens)
	}
	fmt.Println(strings.Repeat("─", 78))
	fmt.Printf("  %d 个 provider · 约 %d tokens\n\n", len(reports), totalTokens)
}

// padDisplay truncates to budget columns and right-pads to exactly that width.
func padDisplay(s string, budget int) string {
	s = truncateDisplay(s, budget)
	if pad := budget - displayWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if isWide(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// truncateDisplay cuts s to at most budget display columns, accounting for CJK
// width so columns don't drift when the detail is Chinese.
//
// The ellipsis costs a column of its own, so the content budget shrinks by one
// whenever truncation actually happens — otherwise the result overflows the
// column by exactly one cell and the whole table shifts.
func truncateDisplay(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if displayWidth(s) <= budget {
		return s
	}
	if budget == 1 {
		return "…"
	}

	limit := budget - 1 // reserve a column for the ellipsis
	width := 0
	var b strings.Builder
	for _, r := range s {
		w := 1
		if isWide(r) {
			w = 2
		}
		if width+w > limit {
			break
		}
		b.WriteRune(r)
		width += w
	}
	b.WriteString("…")
	return b.String()
}

func isWide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) || // Hangul Jamo
		(r >= 0x2E80 && r <= 0xA4CF) || // CJK radicals … Yi
		(r >= 0xAC00 && r <= 0xD7A3) || // Hangul syllables
		(r >= 0xF900 && r <= 0xFAFF) || // CJK compatibility ideographs
		(r >= 0xFE30 && r <= 0xFE6F) || // CJK compatibility forms
		(r >= 0xFF00 && r <= 0xFF60) || // fullwidth forms
		(r >= 0xFFE0 && r <= 0xFFE6)
}

// ---------------------------------------------------------------- model defaults

// Sensible, cheap, currently-available chat models per provider. Override with
// -model when you want to probe something else.
//
// Vendor catalogs churn: models get retired and IDs get renamed. A
// "model not found" from this table means the entry went stale, not that the
// adapter broke — re-run with -model or LLMKIT_MODEL_<PROVIDER>.
var chatModels = map[string]string{
	llmkit.OpenAI:      "gpt-5",
	llmkit.Anthropic:   "claude-sonnet-4-5-20250929",
	llmkit.Gemini:      "gemini-2.5-flash",
	llmkit.XAI:         "grok-4.3",
	llmkit.Mistral:     "mistral-large-latest", // vendor-maintained alias
	llmkit.DeepSeek:    "deepseek-chat",
	llmkit.Moonshot:    "kimi-k2.6", // the k2-*-preview family retired 2026-05-25
	llmkit.Zhipu:       "glm-4.6",
	llmkit.MiniMax:     "MiniMax-M2",
	llmkit.SiliconFlow: "Qwen/Qwen3-8B",
	llmkit.DashScope:   "qwen-plus",
	llmkit.Volcengine:  "doubao-seed-1-6-250615",
	llmkit.Groq:        "openai/gpt-oss-120b",
	llmkit.Together:    "openai/gpt-oss-120b",
	llmkit.Fireworks:   "accounts/fireworks/models/gpt-oss-120b",
	llmkit.Cerebras:    "gpt-oss-120b",
	// Local runtimes serve whatever you pulled or launched, so these are a
	// plausible first guess rather than a catalog entry.
	llmkit.Ollama:     "llama3.2",
	llmkit.VLLM:       "Qwen/Qwen3-8B",
	llmkit.OpenRouter: "openai/gpt-5",
	llmkit.EasyRouter: "gpt-5",
	llmkit.Vercel:     "openai/gpt-5",
}

// An adapter that reports SupportsEmbeddings needs an entry here, or its probe
// comes back SKIP and the claim never gets tested — which is how an adapter ends
// up advertising a route nobody ever called.
//
// A provider absent from both this and embedModelUnknown is a bug, not a SKIP:
// no model ID is better than a guessed one (a wrong ID reads as "embeddings are
// broken" when the endpoint is fine), but the gap has to be recorded on purpose.
var embedModels = map[string]string{
	llmkit.OpenAI:      "text-embedding-3-small",
	llmkit.Gemini:      "text-embedding-004",
	llmkit.SiliconFlow: "BAAI/bge-m3",
	llmkit.Zhipu:       "embedding-3",
	llmkit.Mistral:     "mistral-embed",
	llmkit.MiniMax:     "embo-01",
	llmkit.DashScope:   "text-embedding-v4",
	llmkit.Together:    "BAAI/bge-base-en-v1.5",
	llmkit.Fireworks:   "nomic-ai/nomic-embed-text-v1.5",
	llmkit.Ollama:      "nomic-embed-text",
	llmkit.Vercel:      "openai/text-embedding-3-small",
	llmkit.EasyRouter:  "text-embedding-3-small",
}

// embedModelUnknown lists providers that report SupportsEmbeddings but have no
// entry above, each for a reason. Their embeddings probe reports SKIP.
//
// This is a list of known gaps, not an excuse: it exists so that adding a
// provider forces a decision about its embeddings claim instead of quietly
// inheriting a SKIP. TestEmbedModelsCoverEmbedders fails when a provider claims
// embeddings and appears in neither table.
//
// A provider whose vendor has no usable embeddings route does not belong here —
// it belongs on compat.NoEmbeddings, so it stops claiming the capability. That is
// where moonshot, minimax and volcengine went.
var embedModelUnknown = map[string]string{
	llmkit.VLLM: "一个 vLLM 进程只服务一个模型，没有默认可填",
}

// rerankModels is embedModels' counterpart for the rerank route. Same rule: a
// provider claiming SupportsRerank must appear here or in rerankModelUnknown,
// enforced by TestRerankModelsCoverRerankers.
var rerankModels = map[string]string{
	llmkit.SiliconFlow: "BAAI/bge-reranker-v2-m3",
}

// rerankModelUnknown mirrors embedModelUnknown: providers that claim the
// capability but have no default model worth guessing, each with a reason.
var rerankModelUnknown = map[string]string{}

var imageModels = map[string]string{
	llmkit.OpenAI:     "gpt-image-1",
	llmkit.Gemini:     "gemini-2.5-flash-image",
	llmkit.Vercel:     "openai/gpt-image-1",
	llmkit.OpenRouter: "google/gemini-2.5-flash-image",
	llmkit.EasyRouter: "gpt-image-1",
}

// Each default is overridable per provider so you can probe a model the table
// doesn't know about without rebuilding.
func defaultChatModel(p string) string   { return envOr("LLMKIT_MODEL_", p, chatModels) }
func defaultEmbedModel(p string) string  { return envOr("LLMKIT_EMBED_MODEL_", p, embedModels) }
func defaultRerankModel(p string) string { return envOr("LLMKIT_RERANK_MODEL_", p, rerankModels) }
func defaultImageModel(p string) string  { return envOr("LLMKIT_IMAGE_MODEL_", p, imageModels) }

func envOr(prefix, providerName string, table map[string]string) string {
	key := prefix + strings.ToUpper(strings.ReplaceAll(providerName, "-", "_"))
	if v := os.Getenv(key); v != "" {
		return v
	}
	return table[providerName]
}
