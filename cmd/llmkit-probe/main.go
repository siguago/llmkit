// Command llmkit-probe checks what a provider can actually do with your key.
//
// It makes real API calls and costs real (but tiny) money — every probe caps
// output tokens, and the total is reported when it finishes.
//
//	# one provider, key from the environment or a .env file
//	DEEPSEEK_API_KEY=sk-... llmkit-probe deepseek
//
//	# key inline, specific model
//	llmkit-probe deepseek -key sk-... -model deepseek-reasoner
//
//	# every provider whose key is configured
//	llmkit-probe
//
//	# include image/video generation (noticeably more expensive)
//	llmkit-probe openai -media
//
// A probe reports one of four outcomes:
//
//	PASS   the capability works
//	FAIL   the capability should work but didn't — the reason is printed
//	N/A    the provider or model doesn't offer it (not a defect)
//	SKIP   not attempted (no key, or needs -media)
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/siguago/llmkit"
)

func main() {
	var (
		key      = flag.String("key", "", "API key (default: env var, or .env file)")
		baseURL  = flag.String("base-url", "", "override the API endpoint (relay / private deployment)")
		model    = flag.String("model", "", "chat model to probe (default: a sensible per-provider choice)")
		media    = flag.Bool("media", false, "also probe image/video generation (slower, more expensive)")
		verbose  = flag.Bool("v", false, "print full model responses")
		timeout  = flag.Duration("timeout", 2*time.Minute, "per-probe timeout")
		envFile  = flag.String("env", ".env", "file to read API keys from")
		listOnly = flag.Bool("list", false, "list supported providers and their env vars, then exit")
	)
	flag.Usage = usage
	// Go's flag package stops parsing at the first non-flag argument, so the
	// natural `llmkit-probe deepseek -key sk-...` would silently swallow the
	// flags. Lift a leading provider name out before parsing so both orderings
	// work.
	providerArgs, flagArgs := splitLeadingArgs(os.Args[1:])
	if err := flag.CommandLine.Parse(flagArgs); err != nil {
		os.Exit(2) // flag already printed the error and usage
	}
	providerArgs = append(providerArgs, flag.Args()...)

	if *listOnly {
		listProviders()
		return
	}

	loadEnvFile(*envFile)

	targets, err := resolveTargets(providerArgs, *key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprintln(os.Stderr)
		usage()
		os.Exit(2)
	}

	opts := probeOptions{
		model:   *model,
		baseURL: *baseURL,
		media:   *media,
		verbose: *verbose,
		timeout: *timeout,
	}

	ctx := context.Background()
	var reports []report
	for _, target := range targets {
		reports = append(reports, probeProvider(ctx, target, opts))
	}

	if len(reports) > 1 {
		printGrandTotal(reports)
	}
	for _, r := range reports {
		if r.failed() > 0 {
			os.Exit(1)
		}
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `llmkit-probe — check what a provider can actually do with your key

USAGE
  llmkit-probe [provider] [flags]

  With no provider, probes every provider whose key is configured.

EXAMPLES
  DEEPSEEK_API_KEY=sk-... llmkit-probe deepseek
  llmkit-probe deepseek -key sk-... -model deepseek-reasoner
  llmkit-probe openai -media
  llmkit-probe deepseek -base-url https://my-relay.example/v1
  llmkit-probe                       # everything configured
  llmkit-probe -list                 # supported providers

KEYS
  Read from -key, then the provider's env var, then a .env file
  (KEY=VALUE per line). See -list for the variable names.

FLAGS
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
COST
  Every probe caps output tokens; a full run is a fraction of a cent per
  provider. -media generates real images/video and costs noticeably more.
`)
}

// providerDoesChat reports whether a provider has a chat endpoint at all.
//
// New only builds the adapter — it sends nothing — so a placeholder credential
// is enough to ask, and -list keeps working before any key is configured.
func providerDoesChat(name string) bool {
	c, err := llmkit.New(name, llmkit.WithAPIKey("list-only-placeholder"))
	if err != nil {
		// Unknown provider: say yes rather than mislabel it as video-only.
		return true
	}
	return c.SupportsChat()
}

func listProviders() {
	fmt.Println("provider      env var                     default chat model")
	fmt.Println(strings.Repeat("─", 76))
	for _, name := range llmkit.Providers() {
		configured := " "
		if os.Getenv(llmkit.EnvVar(name)) != "" {
			configured = "✓"
		}
		model := defaultChatModel(name)
		if !providerDoesChat(name) {
			model = "— (仅视频)"
		}
		fmt.Printf("%s %-12s %-27s %s\n", configured, name, llmkit.EnvVar(name), model)
	}
	fmt.Println("\n✓ = key found in the environment")
}

// loadEnvFile reads KEY=VALUE lines into the environment without overwriting
// variables that are already set — an explicit export should always win over a
// checked-in file.
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if name == "" || os.Getenv(name) != "" {
			continue
		}
		_ = os.Setenv(name, value)
	}
	if abs, err := filepath.Abs(path); err == nil {
		fmt.Printf("· keys loaded from %s\n\n", abs)
	}
}

// splitLeadingArgs peels off positional arguments that appear before any flag,
// returning them separately from the rest so flag.Parse sees a clean flag list.
func splitLeadingArgs(args []string) (positional, flags []string) {
	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") {
		i++
	}
	return args[:i], args[i:]
}

type target struct {
	provider string
	key      string
}

// resolveTargets figures out which providers to probe. A named provider must
// have a key; with no name, every provider that has one is probed.
func resolveTargets(args []string, explicitKey string) ([]target, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("probe one provider at a time (got %v)", args)
	}

	if len(args) == 1 {
		name := args[0]
		if llmkit.EnvVar(name) == "" {
			return nil, fmt.Errorf("unknown provider %q — run -list to see the supported names", name)
		}
		key := explicitKey
		if key == "" {
			key = os.Getenv(llmkit.EnvVar(name))
		}
		if key == "" {
			return nil, fmt.Errorf("no key for %s — pass -key, set %s, or put it in .env",
				name, llmkit.EnvVar(name))
		}
		return []target{{provider: name, key: key}}, nil
	}

	if explicitKey != "" {
		return nil, fmt.Errorf("-key needs a provider name, e.g. llmkit-probe deepseek -key sk-...")
	}

	var targets []target
	for _, name := range llmkit.Providers() {
		if key := os.Getenv(llmkit.EnvVar(name)); key != "" {
			targets = append(targets, target{provider: name, key: key})
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no provider keys found — set one (e.g. %s) or pass a provider name with -key",
			llmkit.EnvVar(llmkit.DeepSeek))
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].provider < targets[j].provider })
	return targets, nil
}
