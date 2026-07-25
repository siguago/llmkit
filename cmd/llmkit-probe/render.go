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
	default:
		fmt.Println("  网络或上游异常。如在国内访问海外厂商，检查 HTTPS_PROXY 是否已设置。")
	}
	fmt.Println("\n  （未执行能力探测：凭据不可用时的探测结果没有意义）")
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
var chatModels = map[string]string{
	llmkit.OpenAI:      "gpt-5",
	llmkit.Anthropic:   "claude-sonnet-4-5-20250929",
	llmkit.Gemini:      "gemini-2.5-flash",
	llmkit.DeepSeek:    "deepseek-chat",
	llmkit.Moonshot:    "kimi-k2-turbo-preview",
	llmkit.Zhipu:       "glm-4.6",
	llmkit.MiniMax:     "MiniMax-M2",
	llmkit.SiliconFlow: "Qwen/Qwen3-8B",
	llmkit.DashScope:   "qwen-plus",
	llmkit.Volcengine:  "doubao-seed-1-6-250615",
	llmkit.OpenRouter:  "openai/gpt-5",
	llmkit.EasyRouter:  "gpt-5",
	llmkit.Vercel:      "openai/gpt-5",
}

var embedModels = map[string]string{
	llmkit.OpenAI:      "text-embedding-3-small",
	llmkit.SiliconFlow: "BAAI/bge-m3",
	llmkit.Zhipu:       "embedding-3",
	llmkit.Vercel:      "openai/text-embedding-3-small",
	llmkit.EasyRouter:  "text-embedding-3-small",
}

var imageModels = map[string]string{
	llmkit.OpenAI:     "gpt-image-1",
	llmkit.Gemini:     "gemini-2.5-flash-image",
	llmkit.Vercel:     "openai/gpt-image-1",
	llmkit.OpenRouter: "google/gemini-2.5-flash-image",
	llmkit.EasyRouter: "gpt-image-1",
}

// Each default is overridable per provider so you can probe a model the table
// doesn't know about without rebuilding.
func defaultChatModel(p string) string  { return envOr("LLMKIT_MODEL_", p, chatModels) }
func defaultEmbedModel(p string) string { return envOr("LLMKIT_EMBED_MODEL_", p, embedModels) }
func defaultImageModel(p string) string { return envOr("LLMKIT_IMAGE_MODEL_", p, imageModels) }

func envOr(prefix, providerName string, table map[string]string) string {
	key := prefix + strings.ToUpper(strings.ReplaceAll(providerName, "-", "_"))
	if v := os.Getenv(key); v != "" {
		return v
	}
	return table[providerName]
}
