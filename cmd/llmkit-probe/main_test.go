package main

import (
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/siguago/llmkit"
)

// `llmkit-probe deepseek -key sk-...` is the most natural invocation, and Go's
// flag package stops parsing at the first positional argument — so this split
// is what makes it work at all.
func TestSplitLeadingArgs(t *testing.T) {
	cases := []struct {
		in    []string
		wantP []string
		wantF []string
	}{
		{nil, nil, nil},
		{[]string{"deepseek"}, []string{"deepseek"}, nil},
		{[]string{"-list"}, nil, []string{"-list"}},
		{[]string{"deepseek", "-key", "sk-x"}, []string{"deepseek"}, []string{"-key", "sk-x"}},
		{[]string{"-key", "sk-x", "deepseek"}, nil, []string{"-key", "sk-x", "deepseek"}},
		{[]string{"deepseek", "-v", "-media"}, []string{"deepseek"}, []string{"-v", "-media"}},
	}
	for _, tc := range cases {
		p, f := splitLeadingArgs(tc.in)
		if !slices.Equal(p, tc.wantP) || !slices.Equal(f, tc.wantF) {
			t.Errorf("splitLeadingArgs(%v) = (%v, %v), want (%v, %v)", tc.in, p, f, tc.wantP, tc.wantF)
		}
	}
}

func TestResolveTargets_NamedProvider(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-from-env")

	got, err := resolveTargets([]string{"deepseek"}, "")
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(got) != 1 || got[0].provider != "deepseek" || got[0].key != "sk-from-env" {
		t.Errorf("got %+v", got)
	}

	// An explicit -key must win over the environment.
	got, err = resolveTargets([]string{"deepseek"}, "sk-explicit")
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if got[0].key != "sk-explicit" {
		t.Errorf("key = %q, want the explicit one", got[0].key)
	}
}

func TestResolveTargets_Errors(t *testing.T) {
	// Clear every provider key so "no keys" is the real state.
	for _, name := range llmkit.Providers() {
		t.Setenv(llmkit.EnvVar(name), "")
	}

	cases := []struct {
		name    string
		args    []string
		key     string
		wantSub string
	}{
		{"unknown provider", []string{"nope"}, "sk-x", "unknown provider"},
		{"no key for named provider", []string{"deepseek"}, "", "no key for deepseek"},
		{"two providers", []string{"deepseek", "openai"}, "", "one provider at a time"},
		{"key without provider", nil, "sk-x", "-key needs a provider name"},
		{"nothing configured", nil, "", "no provider keys found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveTargets(tc.args, tc.key)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestResolveTargets_AllConfigured(t *testing.T) {
	for _, name := range llmkit.Providers() {
		t.Setenv(llmkit.EnvVar(name), "")
	}
	t.Setenv("DEEPSEEK_API_KEY", "sk-d")
	t.Setenv("ZHIPU_API_KEY", "sk-z")

	got, err := resolveTargets(nil, "")
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d targets, want 2: %+v", len(got), got)
	}
	// Sorted, so runs are reproducible.
	if got[0].provider != "deepseek" || got[1].provider != "zhipu" {
		t.Errorf("order = %s, %s", got[0].provider, got[1].provider)
	}
}

// A local runtime has no key to configure, so requiring one would make the two
// providers that need none the only two this tool cannot probe.
func TestResolveTargets_KeyOptionalProviderNeedsNoKey(t *testing.T) {
	for _, name := range llmkit.Providers() {
		t.Setenv(llmkit.EnvVar(name), "")
	}

	got, err := resolveTargets([]string{llmkit.Ollama}, "")
	if err != nil {
		t.Fatalf("resolveTargets(ollama) with no key = %v, want success", err)
	}
	if len(got) != 1 || got[0].provider != llmkit.Ollama || got[0].key != "" {
		t.Errorf("got %+v", got)
	}

	// A key is still honored when the runtime sits behind an authenticating proxy.
	t.Setenv("VLLM_API_KEY", "sk-local")
	got, err = resolveTargets([]string{llmkit.VLLM}, "")
	if err != nil {
		t.Fatalf("resolveTargets(vllm): %v", err)
	}
	if got[0].key != "sk-local" {
		t.Errorf("key = %q, want the configured one", got[0].key)
	}
}

// The no-argument sweep must not reach for a local runtime: nothing says whether
// one is up, so including it would make the bare command fail for everyone who
// isn't running one.
func TestResolveTargets_SweepSkipsKeyOptional(t *testing.T) {
	for _, name := range llmkit.Providers() {
		t.Setenv(llmkit.EnvVar(name), "")
	}
	t.Setenv("DEEPSEEK_API_KEY", "sk-d")
	// Set even the optional keys, so exclusion is by policy and not by absence.
	t.Setenv("OLLAMA_API_KEY", "sk-o")
	t.Setenv("VLLM_API_KEY", "sk-v")

	got, err := resolveTargets(nil, "")
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	for _, target := range got {
		if llmkit.KeyOptional(target.provider) {
			t.Errorf("sweep included %q, which must be probed only by name", target.provider)
		}
	}
	if len(got) != 1 || got[0].provider != llmkit.DeepSeek {
		t.Errorf("got %+v, want deepseek only", got)
	}
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := `# a comment
DEEPSEEK_API_KEY=sk-from-file
export ZHIPU_API_KEY="sk-quoted"
OPENAI_API_KEY='sk-single'

MALFORMED_LINE
SPACED_KEY = sk-spaced
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"DEEPSEEK_API_KEY", "ZHIPU_API_KEY", "OPENAI_API_KEY", "SPACED_KEY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	// An already-exported variable must not be clobbered by the file.
	t.Setenv("MOONSHOT_API_KEY", "sk-already-set")

	loadEnvFile(path)

	checks := map[string]string{
		"DEEPSEEK_API_KEY": "sk-from-file",
		"ZHIPU_API_KEY":    "sk-quoted", // double quotes stripped
		"OPENAI_API_KEY":   "sk-single", // single quotes stripped
		"SPACED_KEY":       "sk-spaced", // whitespace around = tolerated
		"MOONSHOT_API_KEY": "sk-already-set",
	}
	for k, want := range checks {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestLoadEnvFile_MissingFileIsSilent(t *testing.T) {
	loadEnvFile(filepath.Join(t.TempDir(), "does-not-exist"))
}

func TestModelDefaults_CoverEveryProvider(t *testing.T) {
	for _, name := range llmkit.Providers() {
		if defaultChatModel(name) == "" {
			t.Errorf("provider %q has no default chat model; -model would be mandatory", name)
		}
	}
}

func TestModelDefaults_EnvOverride(t *testing.T) {
	t.Setenv("LLMKIT_MODEL_DEEPSEEK", "my-model")
	if got := defaultChatModel("deepseek"); got != "my-model" {
		t.Errorf("defaultChatModel = %q, want the env override", got)
	}
	t.Setenv("LLMKIT_EMBED_MODEL_OPENAI", "my-embed")
	if got := defaultEmbedModel("openai"); got != "my-embed" {
		t.Errorf("defaultEmbedModel = %q", got)
	}
	t.Setenv("LLMKIT_IMAGE_MODEL_OPENAI", "my-image")
	if got := defaultImageModel("openai"); got != "my-image" {
		t.Errorf("defaultImageModel = %q", got)
	}
}

// The table must stay aligned when detail strings mix CJK and ASCII — the
// stdlib's %-Ns pads by bytes and drifts badly here.
func TestPadDisplay(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  int // expected display width
	}{
		{"abc", 10, 10},
		{"模型列表", 20, 20},
		{"推理 / thinking", 20, 20},
		{"多模态图像输入", 20, 20},
		{"", 8, 8},
		{"这是一段非常长的中文描述文字需要被截断处理", 12, 12},
		{"a-very-long-ascii-string-that-overflows", 12, 12},
	}
	for _, tc := range cases {
		got := padDisplay(tc.in, tc.width)
		if w := displayWidth(got); w != tc.want {
			t.Errorf("padDisplay(%q, %d) display width = %d, want %d (got %q)",
				tc.in, tc.width, w, tc.want, got)
		}
	}
}

func TestDisplayWidth(t *testing.T) {
	cases := map[string]int{
		"abc":  3,
		"中文":   4,
		"a中b":  4,
		"":     0,
		"PASS": 4,
		"·":    1,
	}
	for in, want := range cases {
		if got := displayWidth(in); got != want {
			t.Errorf("displayWidth(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\n  b\tc"); got != "a b c" {
		t.Errorf("oneLine = %q", got)
	}
	long := strings.Repeat("x", 200)
	got := oneLine(long)
	if len([]rune(got)) > 73 { // 72 + the ellipsis
		t.Errorf("oneLine did not truncate: %d runes", len([]rune(got)))
	}
}

func TestSolidPNG(t *testing.T) {
	data, err := solidPNG(testRed(), 16)
	if err != nil {
		t.Fatalf("solidPNG: %v", err)
	}
	// PNG magic bytes — the vision probe is worthless if this isn't a real image.
	if len(data) < 8 || string(data[1:4]) != "PNG" {
		t.Fatalf("not a PNG: % x", data[:min(8, len(data))])
	}
}

func testRed() color.Color { return color.RGBA{R: 220, G: 30, B: 30, A: 255} }

// Every provider currently does chat — DashScope and Volcengine were the last
// video-only pair and now delegate chat to their vendors' OpenAI-compatible
// endpoints. The video-only branch in -list stays as the guard for whatever
// non-chat adapter comes next; this test is what would catch one being added
// without that branch being exercised.
func TestProviderDoesChat(t *testing.T) {
	videoOnly := map[string]bool{}
	for _, name := range llmkit.Providers() {
		if got, want := providerDoesChat(name), !videoOnly[name]; got != want {
			t.Errorf("providerDoesChat(%q) = %v, want %v", name, got, want)
		}
	}
}

// Every provider needs a default chat model, or `llmkit-probe <name>` opens with
// a model-not-found that looks like a broken adapter.
func TestChatModelsCoverAllProviders(t *testing.T) {
	for _, name := range llmkit.Providers() {
		if !providerDoesChat(name) {
			continue
		}
		if defaultChatModel(name) == "" {
			t.Errorf("chatModels has no entry for %q", name)
		}
	}
}

// A provider that claims embeddings must either have a default model to probe
// with, or be recorded in embedModelUnknown with a reason. Neither means the
// claim ships untested, which is how SupportsEmbeddings starts lying.
func TestEmbedModelsCoverEmbedders(t *testing.T) {
	for _, name := range llmkit.Providers() {
		c, err := llmkit.New(name, llmkit.WithAPIKey("list-only-placeholder"))
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		if !c.SupportsEmbeddings() {
			// The inverse also has to hold: a table entry for a provider that
			// doesn't implement Embedder is dead weight that reads as coverage.
			if defaultEmbedModel(name) != "" {
				t.Errorf("embedModels has an entry for %q, which does not implement Embedder", name)
			}
			continue
		}
		if defaultEmbedModel(name) == "" && embedModelUnknown[name] == "" {
			t.Errorf("%q claims embeddings but has no probe model — add one to embedModels, "+
				"or record why not in embedModelUnknown", name)
		}
	}
}

// TestRerankModelsCoverRerankers is TestEmbedModelsCoverEmbedders for the
// rerank route, and exists for the same reason: the probe's whole promise is
// "configure one key and see what this vendor actually supports". A capability
// that reports true but has no probe model silently inherits a SKIP, which
// reads as coverage it does not have.
//
// This guard was written after rerank shipped without one — the capability was
// added, SupportsRerank answered true, and nothing in the probe could exercise
// it. The gap was invisible precisely because no test asked.
func TestRerankModelsCoverRerankers(t *testing.T) {
	for _, name := range llmkit.Providers() {
		c, err := llmkit.New(name, llmkit.WithAPIKey("list-only-placeholder"))
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		if !c.SupportsRerank() {
			// The inverse also has to hold: a table entry for a provider that
			// doesn't implement Reranker is dead weight that reads as coverage.
			if defaultRerankModel(name) != "" {
				t.Errorf("rerankModels has an entry for %q, which does not implement Reranker", name)
			}
			continue
		}
		if defaultRerankModel(name) == "" && rerankModelUnknown[name] == "" {
			t.Errorf("%q claims rerank but has no probe model — add one to rerankModels, "+
				"or record why not in rerankModelUnknown", name)
		}
	}
}

// -list must work with no credentials configured at all — it is the command you
// run to find out what to configure.
func TestProviderDoesChat_NeedsNoCredential(t *testing.T) {
	for _, name := range llmkit.Providers() {
		t.Setenv(llmkit.EnvVar(name), "")
	}
	if !providerDoesChat("openai") {
		t.Error("providerDoesChat should not depend on a configured key")
	}
}
