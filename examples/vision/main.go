// Command vision sends an image alongside text.
//
//	GEMINI_API_KEY=... go run ./examples/vision https://example.com/photo.jpg
//	GEMINI_API_KEY=... go run ./examples/vision ./local/photo.png
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/siguago/llmkit"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: vision <image-url-or-path>")
	}
	src := os.Args[1]

	client, err := llmkit.New(llmkit.Gemini)
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
		Model: "gemini-2.5-flash",
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
