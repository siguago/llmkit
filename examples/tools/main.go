// Command tools runs a complete tool-calling loop: the model asks for a tool,
// the program answers, the model uses the answer.
//
//	DEEPSEEK_API_KEY=sk-... go run ./examples/tools
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/siguago/llmkit"
)

// getWeather is the "real" implementation behind the tool the model can call.
func getWeather(city string) map[string]any {
	return map[string]any{
		"city":        city,
		"temperature": 26,
		"unit":        "celsius",
		"condition":   "多云",
	}
}

func main() {
	client, err := llmkit.New(llmkit.DeepSeek)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	tools := []llmkit.Tool{
		llmkit.NewTool("get_weather", "查询指定城市的当前天气", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{
					"type":        "string",
					"description": "城市名，例如「杭州」",
				},
			},
			"required": []string{"city"},
		}),
	}

	messages := []llmkit.Message{llmkit.User("杭州现在天气怎么样？适合穿短袖吗？")}

	// A tool loop needs a bound: a model that keeps asking for tools would
	// otherwise spin forever.
	const maxTurns = 5
	for turn := range maxTurns {
		resp, err := client.Chat(ctx, &llmkit.ChatRequest{
			Model:    "deepseek-chat",
			Messages: messages,
			Tools:    tools,
		})
		if err != nil {
			log.Fatal(err)
		}

		calls := llmkit.ResponseToolCalls(resp)
		if len(calls) == 0 {
			fmt.Println("最终回答:", llmkit.ResponseText(resp))
			return
		}

		// Echo the assistant turn back verbatim — the model needs to see its own
		// tool calls in the history for the results to make sense.
		messages = append(messages, *resp.Choices[0].Message)

		for _, call := range calls {
			fmt.Printf("[turn %d] 模型调用 %s(%s)\n", turn+1, call.Function.Name, call.Function.Arguments)

			var args struct {
				City string `json:"city"`
			}
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				messages = append(messages, llmkit.ToolResult(call.ID,
					fmt.Sprintf(`{"error":"bad arguments: %v"}`, err)))
				continue
			}

			switch call.Function.Name {
			case "get_weather":
				messages = append(messages, llmkit.ToolResultJSON(call.ID, getWeather(args.City)))
			default:
				messages = append(messages, llmkit.ToolResult(call.ID,
					`{"error":"unknown tool"}`))
			}
		}
	}
	log.Fatalf("模型在 %d 轮内没有给出最终回答", maxTurns)
}
