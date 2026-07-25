// Command images generates an image and writes it to disk.
//
//	OPENAI_API_KEY=sk-... go run ./examples/images "一只在下棋的柴犬"
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"

	"github.com/siguago/llmkit"
)

func main() {
	prompt := "一只戴着眼镜、在下国际象棋的柴犬，扁平插画风格"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	client, err := llmkit.New(llmkit.OpenAI)
	if err != nil {
		log.Fatal(err)
	}

	n := 1
	resp, err := client.GenerateImage(context.Background(), &llmkit.ImageRequest{
		Model:  "gpt-image-1",
		Prompt: prompt,
		N:      &n,
		Size:   "1024x1024",
		// "inline" asks the adapter to keep base64 payloads in the response.
		// The gateway-oriented "proxy" mode has no meaning outside a gateway.
		Delivery: "inline",
	})
	if err != nil {
		log.Fatal(err)
	}

	for i, asset := range resp.Data {
		switch {
		case asset.B64JSON != "":
			data, err := base64.StdEncoding.DecodeString(asset.B64JSON)
			if err != nil {
				log.Fatalf("asset %d: %v", i, err)
			}
			name := fmt.Sprintf("image-%d.png", i)
			if err := os.WriteFile(name, data, 0o644); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("wrote %s (%d bytes)\n", name, len(data))
		case asset.URL != "":
			// DALL·E returns short-lived URLs rather than inline bytes.
			fmt.Printf("asset %d: %s\n", i, asset.URL)
		default:
			fmt.Printf("asset %d: no payload (%+v)\n", i, asset)
		}
	}

	if u := resp.Usage; u != nil {
		fmt.Printf("images=%d prompt_tokens=%d\n", u.ImageCount, u.PromptTokens)
	}
}
