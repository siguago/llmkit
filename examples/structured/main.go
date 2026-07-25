// Command structured asks for JSON that conforms to a schema, and decodes it.
//
//	OPENAI_API_KEY=sk-... go run ./examples/structured
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/siguago/llmkit"
)

// Recipe is the shape we want back.
type Recipe struct {
	Name        string   `json:"name"`
	Minutes     int      `json:"minutes"`
	Ingredients []string `json:"ingredients"`
	Steps       []string `json:"steps"`
}

func main() {
	client, err := llmkit.New(llmkit.OpenAI)
	if err != nil {
		log.Fatal(err)
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    map[string]any{"type": "string"},
			"minutes": map[string]any{"type": "integer"},
			"ingredients": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"steps": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
		"required":             []string{"name", "minutes", "ingredients", "steps"},
		"additionalProperties": false,
	}

	resp, err := client.Chat(context.Background(), &llmkit.ChatRequest{
		Model:          "gpt-5",
		Messages:       []llmkit.Message{llmkit.User("给我一个番茄炒蛋的菜谱。")},
		ResponseFormat: llmkit.JSONSchemaFormat("recipe", schema),
	})
	if err != nil {
		log.Fatal(err)
	}

	var recipe Recipe
	if err := json.Unmarshal([]byte(llmkit.ResponseText(resp)), &recipe); err != nil {
		log.Fatalf("decode: %v\nraw: %s", err, llmkit.ResponseText(resp))
	}

	fmt.Printf("%s（%d 分钟）\n", recipe.Name, recipe.Minutes)
	fmt.Println("配料:", recipe.Ingredients)
	for i, step := range recipe.Steps {
		fmt.Printf("%d. %s\n", i+1, step)
	}
}
