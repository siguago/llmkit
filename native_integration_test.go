//go:build integration

package llmkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	responsesapi "github.com/siguago/llmkit/protocol/responses"
)

// These tests make real, billable native-protocol calls. They share the
// integration suite's key/model conventions; no key means skip. The longer
// background and thinking cases require an additional explicit opt-in because
// they can run longer and cost more than the basic release smoke tests.

func TestLiveNativeOpenAIResponses(t *testing.T) {
	client := liveClient(t, OpenAI,
		WithTimeout(2*time.Minute),
		WithRetry(NoRetry()),
	)
	model := liveModel(OpenAI)
	ctx := context.Background()

	t.Run("sync_and_token_count", func(t *testing.T) {
		store := false
		response, err := client.CreateResponse(ctx, &responsesapi.CreateRequest{
			Model: model,
			Input: responsesapi.NewTextInput("Reply with exactly: pong"),
			Store: &store,
		})
		if err != nil {
			t.Fatalf("CreateResponse: %v", err)
		}
		if response.ID == "" || !response.IsTerminal() {
			t.Fatalf("incomplete response identity/state: id=%q status=%q", response.ID, response.Status)
		}
		if strings.TrimSpace(response.OutputText()) == "" {
			t.Fatalf("CreateResponse returned no output text: %+v", response)
		}
		if response.Usage == nil || response.Usage.TotalTokens == 0 {
			t.Fatalf("CreateResponse returned no usage: %+v", response.Usage)
		}
		if response.RequestID == "" {
			t.Error("CreateResponse returned no OpenAI request ID")
		}

		count, err := client.CountResponseInputTokens(ctx, &responsesapi.TokenCountRequest{
			Model: model,
			Input: responsesapi.NewTextInput("Reply with exactly: pong"),
		})
		if err != nil {
			t.Fatalf("CountResponseInputTokens: %v", err)
		}
		if count.InputTokens <= 0 || count.RequestID == "" {
			t.Fatalf("invalid input token count: %+v", count)
		}
	})

	t.Run("stream", func(t *testing.T) {
		streamContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		store := false
		stream, err := client.CreateResponseStream(streamContext, &responsesapi.CreateRequest{
			Model: model,
			Input: responsesapi.NewTextInput("Reply with exactly: streamed"),
			Store: &store,
		})
		if err != nil {
			t.Fatalf("CreateResponseStream: %v", err)
		}
		defer stream.Close()

		var sawTerminal bool
		var text strings.Builder
		for {
			event, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				break
			}
			if recvErr != nil {
				t.Fatalf("Recv: %v", recvErr)
			}
			if event.OutputTextDelta != nil {
				text.WriteString(event.OutputTextDelta.Delta)
			}
			sawTerminal = sawTerminal || event.IsTerminal()
		}
		final := stream.FinalResponse()
		if !sawTerminal || final == nil || !final.IsTerminal() {
			t.Fatalf("stream lacked terminal response: saw=%v final=%+v", sawTerminal, final)
		}
		if text.Len() == 0 && strings.TrimSpace(final.OutputText()) == "" {
			t.Fatal("stream returned no output text")
		}
		if stream.RequestID() == "" {
			t.Error("Responses stream returned no OpenAI request ID")
		}
	})

	t.Run("forced_function_and_previous_response", func(t *testing.T) {
		store := true
		maxOutputTokens := 128
		params := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)
		tool := responsesapi.NewFunctionTool(responsesapi.FunctionTool{
			Name: "get_temperature", Description: "Return a city's temperature", Parameters: params,
		})
		first, err := client.CreateResponse(ctx, &responsesapi.CreateRequest{
			Model:           model,
			Input:           responsesapi.NewTextInput("Use get_temperature for Hangzhou."),
			Store:           &store,
			MaxOutputTokens: &maxOutputTokens,
			Tools:           []responsesapi.Tool{tool},
			ToolChoice:      json.RawMessage(`{"type":"function","name":"get_temperature"}`),
		})
		if err != nil {
			t.Fatalf("forced function response: %v", err)
		}
		defer deleteLiveResponse(t, client, first.ID)
		calls := first.FunctionCalls()
		if len(calls) != 1 || calls[0].CallID == "" || calls[0].Name != "get_temperature" {
			t.Fatalf("unexpected function calls: %+v", calls)
		}

		retrieved, err := client.RetrieveResponse(ctx, first.ID, nil)
		if err != nil || retrieved.ID != first.ID {
			t.Fatalf("RetrieveResponse: response=%+v err=%v", retrieved, err)
		}
		items, err := client.ListResponseInputItems(ctx, first.ID, &responsesapi.ListInputItemsOptions{Limit: intPtr(20)})
		if err != nil || len(items.Data) == 0 {
			t.Fatalf("ListResponseInputItems: items=%+v err=%v", items, err)
		}

		previousID := first.ID
		second, err := client.CreateResponse(ctx, &responsesapi.CreateRequest{
			Model: model,
			Input: responsesapi.NewItemInput(responsesapi.NewFunctionCallOutputItem(responsesapi.FunctionCallOutput{
				CallID: calls[0].CallID,
				Output: responsesapi.NewTextContent(`{"city":"Hangzhou","celsius":23}`),
			})),
			PreviousResponseID: &previousID,
			Store:              &store,
			MaxOutputTokens:    &maxOutputTokens,
			Tools:              []responsesapi.Tool{tool},
			ToolChoice:         json.RawMessage(`"none"`),
		})
		if err != nil {
			t.Fatalf("previous_response_id continuation: %v", err)
		}
		defer deleteLiveResponse(t, client, second.ID)
		if second.PreviousResponseID == nil || *second.PreviousResponseID != first.ID {
			t.Fatalf("previous response link lost: %+v", second.PreviousResponseID)
		}
		if strings.TrimSpace(second.OutputText()) == "" {
			t.Fatalf("tool result continuation returned no text: %+v", second)
		}
	})

	t.Run("background_retrieve_cancel", func(t *testing.T) {
		if os.Getenv("LLMKIT_RUN_NATIVE_BACKGROUND") != "1" {
			t.Skip("set LLMKIT_RUN_NATIVE_BACKGROUND=1 for the longer background/cancel release check")
		}
		store, background := true, true
		maxOutputTokens := 512
		response, err := client.CreateResponse(ctx, &responsesapi.CreateRequest{
			Model:           model,
			Input:           responsesapi.NewTextInput("Write a detailed but finite explanation of distributed consensus."),
			Store:           &store,
			Background:      &background,
			MaxOutputTokens: &maxOutputTokens,
		})
		if err != nil {
			t.Fatalf("background CreateResponse: %v", err)
		}
		defer deleteLiveResponse(t, client, response.ID)

		current, err := client.RetrieveResponse(ctx, response.ID, nil)
		if err != nil {
			t.Fatalf("retrieve background response: %v", err)
		}
		if !current.IsTerminal() {
			cancelled, cancelErr := client.CancelResponse(ctx, current.ID)
			if cancelErr != nil {
				// A short request can win the race with cancellation. Confirm that
				// it actually reached a terminal state instead of masking a cancel bug.
				current, err = client.RetrieveResponse(ctx, current.ID, nil)
				if err != nil || !current.IsTerminal() {
					t.Fatalf("CancelResponse: %v; subsequent retrieve=%+v err=%v", cancelErr, current, err)
				}
			} else {
				current = cancelled
			}
		}
		final, err := client.WaitResponse(ctx, current, &WaitResponseOptions{
			Interval: 250 * time.Millisecond,
			Timeout:  2 * time.Minute,
		})
		if err != nil || final == nil || !final.IsTerminal() {
			t.Fatalf("WaitResponse: final=%+v err=%v", final, err)
		}
	})
}

func TestLiveNativeAnthropicMessages(t *testing.T) {
	client := liveClient(t, Anthropic,
		WithTimeout(2*time.Minute),
		WithRetry(NoRetry()),
	)
	model := liveModel(Anthropic)
	ctx := context.Background()

	t.Run("sync_stream_and_token_count", func(t *testing.T) {
		request := &anthropicapi.MessageRequest{
			Model:     model,
			MaxTokens: 128,
			Messages:  anthropicUserMessages("Reply with exactly: pong"),
		}
		message, err := client.CreateAnthropicMessage(ctx, request)
		if err != nil {
			t.Fatalf("CreateAnthropicMessage: %v", err)
		}
		if message.ID == "" || strings.TrimSpace(message.Text()) == "" || message.Usage.OutputTokens == 0 {
			t.Fatalf("incomplete Anthropic message: %+v", message)
		}
		if message.RequestID == "" {
			t.Error("Anthropic message returned no request ID")
		}

		count, err := client.CountAnthropicMessageTokens(ctx, &anthropicapi.TokenCountRequest{
			Model:    model,
			Messages: request.Messages,
		})
		if err != nil {
			t.Fatalf("CountAnthropicMessageTokens: %v", err)
		}
		if count.InputTokens <= 0 || count.RequestID == "" {
			t.Fatalf("invalid Anthropic token count: %+v", count)
		}

		streamContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		stream, err := client.CreateAnthropicMessageStream(streamContext, &anthropicapi.MessageRequest{
			Model:     model,
			MaxTokens: 128,
			Messages:  anthropicUserMessages("Reply with exactly: streamed"),
		})
		if err != nil {
			t.Fatalf("CreateAnthropicMessageStream: %v", err)
		}
		defer stream.Close()
		var sawStop bool
		for {
			event, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				break
			}
			if recvErr != nil {
				t.Fatalf("Recv: %v", recvErr)
			}
			sawStop = sawStop || event.Type == anthropicapi.EventTypeMessageStop
		}
		final := stream.FinalMessage()
		if !sawStop || final == nil || strings.TrimSpace(final.Text()) == "" {
			t.Fatalf("stream lacked final message: saw_stop=%v final=%+v", sawStop, final)
		}
		if stream.RequestID() == "" {
			t.Error("Anthropic stream returned no request ID")
		}
	})

	t.Run("forced_tool_round_trip", func(t *testing.T) {
		name := "get_temperature"
		description := "Return a city's temperature"
		tool := anthropicapi.Tool{
			Name:        name,
			Description: &description,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		}
		first, err := client.CreateAnthropicMessage(ctx, &anthropicapi.MessageRequest{
			Model:      model,
			MaxTokens:  256,
			Messages:   anthropicUserMessages("Use get_temperature for Hangzhou."),
			Tools:      []anthropicapi.Tool{tool},
			ToolChoice: &anthropicapi.ToolChoice{Type: "tool", Name: &name},
		})
		if err != nil {
			t.Fatalf("forced tool message: %v", err)
		}
		uses := anthropicapi.ToolUses(first)
		if len(uses) != 1 || uses[0].ID == "" || uses[0].Name != name {
			t.Fatalf("unexpected tool_use blocks: %+v", uses)
		}
		resultContent := anthropicapi.StringContent(`{"city":"Hangzhou","celsius":23}`)
		second, err := client.CreateAnthropicMessage(ctx, &anthropicapi.MessageRequest{
			Model:     model,
			MaxTokens: 256,
			Messages: []anthropicapi.MessageParam{
				{Role: anthropicapi.RoleUser, Content: anthropicapi.StringContent("Use get_temperature for Hangzhou.")},
				{Role: anthropicapi.RoleAssistant, Content: anthropicapi.BlockContent(first.Content...)},
				{Role: anthropicapi.RoleUser, Content: anthropicapi.BlockContent(
					anthropicapi.NewToolResultBlock(uses[0].ID, &resultContent, nil),
				)},
			},
			Tools:      []anthropicapi.Tool{tool},
			ToolChoice: &anthropicapi.ToolChoice{Type: "none"},
		})
		if err != nil {
			t.Fatalf("tool_result continuation: %v", err)
		}
		if strings.TrimSpace(second.Text()) == "" {
			t.Fatalf("tool result continuation returned no text: %+v", second)
		}
	})

	t.Run("thinking_signature_round_trip", func(t *testing.T) {
		if os.Getenv("LLMKIT_RUN_NATIVE_THINKING") != "1" {
			t.Skip("set LLMKIT_RUN_NATIVE_THINKING=1 for the longer thinking-signature release check")
		}
		budget := int64(1024)
		thinking := &anthropicapi.ThinkingConfig{Type: "enabled", BudgetTokens: &budget}
		firstPrompt := "Think carefully: what is 37 times 41? Give the final integer."
		first, err := client.CreateAnthropicMessage(ctx, &anthropicapi.MessageRequest{
			Model:     model,
			MaxTokens: 1400,
			Messages:  anthropicUserMessages(firstPrompt),
			Thinking:  thinking,
		})
		if err != nil {
			t.Fatalf("thinking message: %v", err)
		}
		var signature string
		for _, block := range first.Content {
			if block.Thinking != nil && block.Thinking.Signature != "" {
				signature = block.Thinking.Signature
				break
			}
		}
		if signature == "" {
			t.Fatalf("thinking response contained no signature: %+v", first.Content)
		}
		_, err = client.CreateAnthropicMessage(ctx, &anthropicapi.MessageRequest{
			Model:     model,
			MaxTokens: 1400,
			Messages: []anthropicapi.MessageParam{
				{Role: anthropicapi.RoleUser, Content: anthropicapi.StringContent(firstPrompt)},
				{Role: anthropicapi.RoleAssistant, Content: anthropicapi.BlockContent(first.Content...)},
				{Role: anthropicapi.RoleUser, Content: anthropicapi.StringContent("Add one to that result.")},
			},
			Thinking: thinking,
		})
		if err != nil {
			t.Fatalf("thinking signature continuation: %v", err)
		}
	})
}

func TestLiveNativeInvalidCredentials(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		if os.Getenv(EnvVar(OpenAI)) == "" {
			t.Skipf("%s not set; key presence is the integration opt-in", EnvVar(OpenAI))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client, err := New(OpenAI, WithAPIKey("llmkit-intentionally-invalid"), WithRetry(NoRetry()))
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.CountResponseInputTokens(ctx, &responsesapi.TokenCountRequest{
			Model: liveModel(OpenAI), Input: responsesapi.NewTextInput("hello"),
		})
		if err == nil || !IsAuthError(err) {
			t.Fatalf("invalid OpenAI key: want auth error, got %v", err)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		if os.Getenv(EnvVar(Anthropic)) == "" {
			t.Skipf("%s not set; key presence is the integration opt-in", EnvVar(Anthropic))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client, err := New(Anthropic, WithAPIKey("llmkit-intentionally-invalid"), WithRetry(NoRetry()))
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.CountAnthropicMessageTokens(ctx, &anthropicapi.TokenCountRequest{
			Model: liveModel(Anthropic), Messages: anthropicUserMessages("hello"),
		})
		if err == nil || !IsAuthError(err) {
			t.Fatalf("invalid Anthropic key: want auth error, got %v", err)
		}
	})
}

func anthropicUserMessages(text string) []anthropicapi.MessageParam {
	return []anthropicapi.MessageParam{{
		Role: anthropicapi.RoleUser, Content: anthropicapi.StringContent(text),
	}}
}

func deleteLiveResponse(t *testing.T, client *Client, responseID string) {
	t.Helper()
	if responseID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := client.DeleteResponse(ctx, responseID); err != nil && !IsNotFound(err) {
		t.Logf("cleanup DeleteResponse(%s): %v", responseID, err)
	}
}

func intPtr(value int) *int { return &value }
