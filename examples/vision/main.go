// Command vision sends an image alongside text.
//
//	GEMINI_API_KEY=... go run ./examples/vision https://example.com/photo.jpg
//	DEEPSEEK_API_KEY=... go run ./examples/vision -provider deepseek ./local/photo.png
//	MISTRAL_API_KEY=... go run ./examples/vision -provider mistral -model pixtral-large-latest ./photo.png
//
// The model default is derived from -provider for the two providers this
// example ships defaults for; any other provider needs an explicit -model.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/siguago/llmkit"
)

func main() {
	providerName := flag.String("provider", llmkit.Gemini, "provider name")
	model := flag.String("model", "", "model ID (default: gemini-2.5-flash for gemini, deepseek-flash for deepseek; required for other providers)")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: vision [-provider name] [-model id] <image-url-or-path>")
	}
	src := flag.Arg(0)

	if *model == "" {
		defaultModel, err := defaultVisionModel(*providerName)
		if err != nil {
			log.Fatal(err)
		}
		*model = defaultModel
	}

	client, err := llmkit.New(*providerName)
	if err != nil {
		log.Fatal(err)
	}

	// An https URL goes through as-is; a local file is inlined as a data URI.
	var image llmkit.ContentPart
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		image = llmkit.Image(src)
	} else {
		data, err := os.ReadFile(src)
		if err != nil {
			log.Fatal(err)
		}
		image = llmkit.ImageBytes(mimeFor(src), data)
	}

	resp, err := client.Chat(context.Background(), &llmkit.ChatRequest{
		Model: *model,
		Messages: []llmkit.Message{
			llmkit.UserWith(
				llmkit.Text("这张图里有什么？用中文简要描述。"),
				image,
			),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(llmkit.ResponseText(resp))
}

// defaultVisionModel derives the model from the provider. Hardcoding Gemini's
// model ID made `-provider deepseek` send the wrong model and fail with a 404;
// a provider without a known vision model must name one explicitly.
func defaultVisionModel(providerName string) (string, error) {
	switch providerName {
	case llmkit.Gemini:
		return "gemini-2.5-flash", nil
	case llmkit.DeepSeek:
		return "deepseek-flash", nil
	default:
		return "", fmt.Errorf("provider %q has no default vision model; pass -model <id> (see README for model names)", providerName)
	}
}

func mimeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}
