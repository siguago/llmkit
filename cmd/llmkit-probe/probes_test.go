package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/siguago/llmkit"
	"github.com/siguago/llmkit/provider"
)

type fakeProbeChat struct {
	provider.Provider
	name      string
	requests  []*provider.ChatCompletionRequest
	chat      func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error)
	stream    provider.StreamReader
	streamErr error
}

func (f *fakeProbeChat) Name() string { return f.name }

func (f *fakeProbeChat) ChatCompletion(_ context.Context, _, _ string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	f.requests = append(f.requests, req)
	return f.chat(len(f.requests)-1, req)
}

func (f *fakeProbeChat) ChatCompletionStream(_ context.Context, _, _ string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	f.requests = append(f.requests, req)
	return f.stream, f.streamErr
}

func probeClient(t *testing.T, p provider.Provider) *llmkit.Client {
	t.Helper()
	c, err := llmkit.Wrap(p, llmkit.WithAPIKey("offline-key"), llmkit.WithRetry(llmkit.NoRetry()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func probeResponse(text, reasoning, finish string, usage *llmkit.Usage) *llmkit.ChatResponse {
	return &llmkit.ChatResponse{
		Choices: []provider.Choice{{
			Message:      &llmkit.Message{Role: "assistant", Content: text, ReasoningContent: &reasoning},
			FinishReason: &finish,
		}},
		Usage: usage,
	}
}

func TestDeepSeekDefaultUsesCurrentModel(t *testing.T) {
	t.Setenv("LLMKIT_MODEL_DEEPSEEK", "")
	if got := defaultChatModel(llmkit.DeepSeek); got != "deepseek-flash" {
		t.Fatalf("default model = %q, want deepseek-flash", got)
	}
}

func TestProbeChatDistinguishesIncompleteAndEmptyResponses(t *testing.T) {
	reasoningUsage := &llmkit.Usage{ReasoningTokens: 512, TotalTokens: 520}
	tests := []struct {
		name string
		resp *llmkit.ChatResponse
		want outcome
	}{
		{"answer", probeResponse("幂等操作重复执行的结果一致。", "", "stop", nil), pass},
		{"reasoning trace only", probeResponse("", "let me think", "length", nil), incomplete},
		{"reasoning usage only", probeResponse("", "", "length", reasoningUsage), incomplete},
		{"partial answer", probeResponse("尚未完成", "let me think", "length", reasoningUsage), incomplete},
		{"partial answer without reasoning", probeResponse("尚未完成", "", "length", nil), incomplete},
		{"empty stop", probeResponse("", "", "stop", nil), fail},
		// Budget exhaustion with no visible output is INCOMPLETE: the capability
		// is unverified, not broken. A clean stop with nothing remains a failure.
		{"empty length without reasoning", probeResponse("", "", "length", nil), incomplete},
		{"reasoning without length", probeResponse("", "thinking", "stop", reasoningUsage), fail},
		{"whitespace", probeResponse(" \n", "", "stop", nil), fail},
		{"nil response", nil, fail},
		{"missing choices", &llmkit.ChatResponse{Usage: reasoningUsage}, fail},
		{"missing message", &llmkit.ChatResponse{Choices: []provider.Choice{{}}, Usage: reasoningUsage}, fail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
				return tt.resp, nil
			}}
			res := probeChat(context.Background(), probeClient(t, p), "deepseek-flash")
			if res.outcome != tt.want {
				t.Fatalf("result = %+v, want outcome %v", res, tt.want)
			}
			if len(p.requests) != 1 {
				t.Fatalf("calls = %d, want one attempt", len(p.requests))
			}
			if tt.want == incomplete && !strings.Contains(res.detail, "finish_reason=length") {
				t.Errorf("missing budget evidence: %+v", res)
			}
			if tt.resp != nil && res.usage != tt.resp.Usage {
				t.Error("response usage was lost")
			}
		})
	}
}

func TestTextProbesKeepReasoningBudgetExhaustionUnverified(t *testing.T) {
	for name, run := range map[string]func(context.Context, *llmkit.Client, string) result{
		"multiturn": probeMultiTurn,
		"vision": func(ctx context.Context, c *llmkit.Client, model string) result {
			return probeVision(ctx, c, model, "")
		},
		"structured": probeStructured, "tools": probeTools,
	} {
		t.Run(name, func(t *testing.T) {
			p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
				return probeResponse("", "reasoning", "length", &llmkit.Usage{TotalTokens: 512}), nil
			}}
			res := run(context.Background(), probeClient(t, p), "deepseek-flash")
			if res.outcome != incomplete || len(p.requests) != 1 {
				t.Fatalf("result = %+v, requests = %d; want one incomplete attempt", res, len(p.requests))
			}
		})
	}
}

func TestProbeToolsDoesNotReplayTruncatedToolArguments(t *testing.T) {
	for _, reasoning := range []string{"", "thinking"} {
		t.Run(reasoning, func(t *testing.T) {
			usage := &llmkit.Usage{TotalTokens: 512}
			first := probeResponse("", reasoning, "length", usage)
			first.Choices[0].Message.ToolCalls = []llmkit.ToolCall{{
				ID: "call-1", Type: "function",
				Function: provider.ToolCallFunction{Name: "get_temperature", Arguments: `{"city":"杭`},
			}}
			p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(i int, _ *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
				if i != 0 {
					t.Fatal("truncated tool arguments were replayed to the model")
				}
				return first, nil
			}}
			res := probeTools(context.Background(), probeClient(t, p), "deepseek-flash")
			if res.outcome != incomplete || res.usage != usage || len(p.requests) != 1 {
				t.Fatalf("result=%+v requests=%d", res, len(p.requests))
			}
		})
	}
}

func TestTextProbesRejectMalformedOrGenuinelyEmptyResponses(t *testing.T) {
	for name, run := range map[string]func(context.Context, *llmkit.Client, string) result{
		"chat": probeChat, "multiturn": probeMultiTurn,
		"vision": func(ctx context.Context, c *llmkit.Client, model string) result {
			return probeVision(ctx, c, model, "")
		},
		"structured": probeStructured, "tools": probeTools,
	} {
		t.Run(name, func(t *testing.T) {
			for _, resp := range []*llmkit.ChatResponse{nil, {}, {Choices: []provider.Choice{{}}}, probeResponse("", "", "stop", nil)} {
				p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) { return resp, nil }}
				res := run(context.Background(), probeClient(t, p), "deepseek-flash")
				if res.outcome != fail || len(p.requests) != 1 {
					t.Fatalf("result=%+v requests=%d, want one failed attempt", res, len(p.requests))
				}
			}
		})
	}
}

func TestProbeMultiTurnValidatesBothTurnsAndPreservesUsage(t *testing.T) {
	for _, first := range []*llmkit.ChatResponse{nil, {}, {Choices: []provider.Choice{{}}}, probeResponse("", "", "stop", nil)} {
		p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
			return first, nil
		}}
		res := probeMultiTurn(context.Background(), probeClient(t, p), "deepseek-flash")
		if res.outcome != fail || len(p.requests) != 1 || !strings.Contains(res.detail, "第 1 轮") {
			t.Fatalf("invalid first turn: result=%+v, calls=%d", res, len(p.requests))
		}
	}
	for _, second := range []*llmkit.ChatResponse{nil, probeResponse("", "", "stop", nil), probeResponse("", "reasoning", "length", &llmkit.Usage{TotalTokens: 512})} {
		p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(i int, _ *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
			if i == 0 {
				return probeResponse("记住了", "first reasoning", "stop", &llmkit.Usage{TotalTokens: 10}), nil
			}
			return second, nil
		}}
		res := probeMultiTurn(context.Background(), probeClient(t, p), "deepseek-flash")
		want, tokens := fail, 10
		if second != nil && second.Usage != nil {
			want, tokens = incomplete, 522
		}
		if res.outcome != want || res.usage == nil || res.usage.TotalTokens != tokens || len(p.requests) != 2 {
			t.Fatalf("second turn: result=%+v, calls=%d", res, len(p.requests))
		}
		if got := p.requests[1].Messages[1].ReasoningContent; got == nil || *got != "first reasoning" {
			t.Fatal("assistant round-trip lost reasoning")
		}
	}
}

func TestProbeChatBudgetsCoverEveryRequest(t *testing.T) {
	tests := []struct {
		name  string
		run   func(context.Context, *llmkit.Client, string) result
		calls int
	}{
		{"chat", probeChat, 1},
		{"vision", func(ctx context.Context, c *llmkit.Client, model string) result {
			return probeVision(ctx, c, model, "")
		}, 1},
		{"multiturn", probeMultiTurn, 2},
		{"tools", probeTools, 2}, {"structured", probeStructured, 1}, {"structured fallback", probeStructured, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(i int, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
				if tt.name == "structured fallback" && i == 0 {
					return nil, &provider.ProviderError{StatusCode: 422, Message: "schema unsupported"}
				}
				if tt.name == "tools" && i == 0 {
					resp := probeResponse("", "", "tool_calls", nil)
					resp.Choices[0].Message.ToolCalls = []llmkit.ToolCall{{ID: "call-1", Type: "function", Function: provider.ToolCallFunction{Name: "get_temperature", Arguments: `{"city":"杭州"}`}}}
					return resp, nil
				}
				answer := "42, 26"
				if tt.name == "vision" {
					answer = "红色"
				}
				if req.ResponseFormat != nil {
					answer = `{"city":"杭州","celsius":26}`
				}
				return probeResponse(answer, "", "stop", nil), nil
			}}
			res := tt.run(context.Background(), probeClient(t, p), "deepseek-flash")
			if res.outcome != pass || len(p.requests) != tt.calls {
				t.Fatalf("result=%+v calls=%d want=%d", res, len(p.requests), tt.calls)
			}
			for i, req := range p.requests {
				wantBudget := 512
				if tt.name == "vision" {
					wantBudget = 2048
				}
				if req.MaxTokens == nil || *req.MaxTokens != wantBudget {
					t.Errorf("request %d must use the bounded %d-token budget: %+v", i, wantBudget, req)
				}
			}
		})
	}
}

func TestProbeVisionRefusalIsNotApplicable(t *testing.T) {
	// A 400/404/422 refusal means "this model does not take images" whether the
	// adapter rejected it locally or the vendor answered, and whether or not the
	// adapter recorded an origin. That must read as N/A, not FAIL: the tool's
	// core run is against each vendor's default chat model, and most of those
	// are text-only.
	for _, origin := range []provider.ErrorOrigin{provider.ErrorOriginLocal, provider.ErrorOriginUpstream, provider.ErrorOriginUnknown} {
		for _, model := range []string{"deepseek-flash", "deepseek-v4-pro", "future-model"} {
			t.Run(string(origin)+"/"+model, func(t *testing.T) {
				err := provider.WithErrorOrigin(&provider.ProviderError{StatusCode: 422, Message: "image refusal details"}, origin)
				p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) { return nil, err }}
				res := probeVision(context.Background(), probeClient(t, p), "deepseek-flash", "")
				if res.outcome != notApplicable {
					t.Fatalf("result=%+v, want N/A — a refusal is not a defect", res)
				}
				for _, want := range []string{
					"image refusal details", "该模型可能不支持图像输入", "-vision-model", "LLMKIT_VISION_MODEL_DEEPSEEK",
				} {
					if !strings.Contains(res.detail, want) {
						t.Errorf("detail is missing %q: %q", want, res.detail)
					}
				}
				if res.body != err.Error() {
					t.Errorf("body = %q, want the raw error text", res.body)
				}
			})
		}
	}

	// Failures that do not establish a capability gap stay failures: a bad key,
	// throttling and an unhealthy upstream all mean the probe learned nothing.
	for _, status := range []int{401, 403, 429, 500, 503} {
		t.Run(fmt.Sprint(status)+"/fail", func(t *testing.T) {
			err := provider.WithErrorOrigin(&provider.ProviderError{StatusCode: status, Message: "probe diagnostic"}, provider.ErrorOriginUpstream)
			p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) { return nil, err }}
			res := probeVision(context.Background(), probeClient(t, p), "deepseek-flash", "")
			if res.outcome != fail || !strings.Contains(res.body, "probe diagnostic") {
				t.Fatalf("result=%+v, want FAIL with the original error", res)
			}
		})
	}

	// Failures that are not refusals stay failures: a local problem that is not
	// a request rejection, and a transport error with no HTTP status at all.
	for name, err := range map[string]error{
		"local non-classified": provider.WithErrorOrigin(errors.New("marshal failed"), provider.ErrorOriginLocal),
		"network":              errors.New("dial tcp 203.0.113.1:443: connect: connection refused"),
	} {
		t.Run(name+"/fail", func(t *testing.T) {
			p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
				return nil, err
			}}
			if res := probeVision(context.Background(), probeClient(t, p), "deepseek-flash", ""); res.outcome != fail {
				t.Fatalf("result=%+v, want FAIL", res)
			}
		})
	}
}

func TestProbeVisionAnswers(t *testing.T) {
	for _, answer := range []string{
		// Pure color names still pass.
		"红", "红色", "red", "RED.", "红色！",
		// Correct answers in full sentences — the answers models actually give.
		"这张图片是红色的。",
		"图中的圆形是红色。",
		"The image is red.",
		"It's a red square.",
		"这是一张纯红色的图片",
		"图片主体为红色（red）。",
		"非常红",
	} {
		t.Run("pass/"+answer, func(t *testing.T) {
			p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
				return probeResponse(answer, "", "stop", nil), nil
			}}
			if res := probeVision(context.Background(), probeClient(t, p), "deepseek-flash", ""); res.outcome != pass {
				t.Fatalf("answer=%q result=%+v, want PASS", answer, res)
			}
		})
	}
	for _, answer := range []string{
		"", " \n", "蓝色", "green", "不是红色", "非红色", "not red",
		"green, not red", "红色，不是蓝色", "hundred", "colored",
		"I cannot see the image.",
	} {
		t.Run("fail/"+answer, func(t *testing.T) {
			p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
				return probeResponse(answer, "", "stop", nil), nil
			}}
			if res := probeVision(context.Background(), probeClient(t, p), "deepseek-flash", ""); res.outcome != fail {
				t.Fatalf("answer=%q result=%+v, want FAIL", answer, res)
			}
		})
	}
}

func TestProbeVisionHTTPStatusClassification(t *testing.T) {
	serve := func(t *testing.T, status int) (*httptest.Server, *int) {
		t.Helper()
		calls := new(int)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*calls++
			var body struct {
				Model     string `json:"model"`
				MaxTokens int    `json:"max_tokens"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Model != "deepseek-flash" || body.MaxTokens != probeVisionMaxTokens {
				t.Errorf("unexpected request: %+v", body)
			}
			w.WriteHeader(status)
			// Short enough to survive describeErr's one-line truncation, so the
			// detail can be checked for the upstream text itself.
			fmt.Fprint(w, `{"error":{"message":"no vision"}}`)
		}))
		t.Cleanup(server.Close)
		return server, calls
	}
	for _, status := range []int{400, 404, 422} {
		t.Run("refusal/"+fmt.Sprint(status), func(t *testing.T) {
			server, calls := serve(t, status)
			client, err := llmkit.New(llmkit.DeepSeek, llmkit.WithAPIKey("offline-key"), llmkit.WithBaseURL(server.URL), llmkit.WithRetry(llmkit.NoRetry()))
			if err != nil {
				t.Fatal(err)
			}
			res := probeVision(context.Background(), client, "deepseek-flash", "")
			if res.outcome != notApplicable || *calls != 1 || !strings.Contains(res.detail, "该模型可能不支持图像输入") ||
				!strings.Contains(res.detail, "上游返回") || !strings.Contains(res.detail, "no vision") ||
				!strings.Contains(res.body, "no vision") {
				t.Fatalf("result=%+v calls=%d", res, *calls)
			}
		})
	}
	for _, status := range []int{401, 429, 500} {
		t.Run("failure/"+fmt.Sprint(status), func(t *testing.T) {
			server, calls := serve(t, status)
			client, err := llmkit.New(llmkit.DeepSeek, llmkit.WithAPIKey("offline-key"), llmkit.WithBaseURL(server.URL), llmkit.WithRetry(llmkit.NoRetry()))
			if err != nil {
				t.Fatal(err)
			}
			res := probeVision(context.Background(), client, "deepseek-flash", "")
			if res.outcome != fail || *calls != 1 || !strings.Contains(res.detail, "上游返回") {
				t.Fatalf("status=%d result=%+v calls=%d, want FAIL", status, res, *calls)
			}
		})
	}
}

func TestDescribeErrOnlyNamesEstablishedOrigins(t *testing.T) {
	for _, status := range []int{422, 500} {
		local := provider.WithErrorOrigin(&provider.ProviderError{StatusCode: status, Message: "diagnostic"}, provider.ErrorOriginLocal)
		if got := describeErr(fmt.Errorf("wrapped: %w", local)); !strings.HasPrefix(got, "本地拒绝：") {
			t.Errorf("local status=%d detail=%q", status, got)
		}
		upstream := provider.WithErrorOrigin(&provider.ProviderError{StatusCode: status, Message: "diagnostic"}, provider.ErrorOriginUpstream)
		if got := describeErr(fmt.Errorf("wrapped: %w", upstream)); !strings.HasPrefix(got, "上游返回：") {
			t.Errorf("upstream status=%d detail=%q", status, got)
		}
		// Unmarked errors get no "来源未知：" prefix — it was noise the reader
		// could not act on.
		unmarked := &provider.ProviderError{StatusCode: status, Message: "diagnostic"}
		got := describeErr(fmt.Errorf("wrapped: %w", unmarked))
		if strings.Contains(got, "来源") || strings.Contains(got, "上游返回") || strings.Contains(got, "本地拒绝") {
			t.Errorf("unmarked status=%d detail=%q", status, got)
		}
		if strings.HasPrefix(got, "来源未知") {
			t.Errorf("unmarked status=%d still carries the unknown-origin prefix: %q", status, got)
		}
	}
}

func TestProbeVisionHonorsVisionModelOverrides(t *testing.T) {
	for _, tt := range []struct {
		name        string
		env         string
		flag        string
		chatModel   string
		wantRequest string
	}{
		{"default chat model", "", "", "chat-model", "chat-model"},
		{"env override", "deepseek-vision", "", "chat-model", "deepseek-vision"},
		{"env overrides explicit -model", "deepseek-vision", "", "-model-value", "deepseek-vision"},
		{"-vision-model wins", "deepseek-vision", "explicit-vision", "chat-model", "explicit-vision"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LLMKIT_VISION_MODEL_DEEPSEEK", tt.env)
			p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
				return probeResponse("红色", "", "stop", nil), nil
			}}
			c := probeClient(t, p)
			var visionProbe probe
			for _, candidate := range buildProbes(c, tt.chatModel, target{provider: llmkit.DeepSeek}, probeOptions{visionModel: tt.flag}) {
				if candidate.name == "多模态图像输入" {
					visionProbe = candidate
					break
				}
			}
			if visionProbe.run == nil {
				t.Fatal("vision probe missing from the real probe list")
			}
			if res := visionProbe.run(context.Background()); res.outcome != pass {
				t.Fatalf("result=%+v, want PASS", res)
			}
			if len(p.requests) != 1 || p.requests[0].Model != tt.wantRequest {
				t.Fatalf("requests=%+v, want model %q", p.requests, tt.wantRequest)
			}
		})
	}
}

type fakeProbeStream struct {
	chunks []*provider.ChatCompletionChunk
	usage  *provider.Usage
	err    error
	closed bool
}

func (s *fakeProbeStream) Recv() (*provider.ChatCompletionChunk, error) {
	if len(s.chunks) == 0 {
		if s.err != nil {
			return nil, s.err
		}
		return nil, io.EOF
	}
	chunk := s.chunks[0]
	s.chunks = s.chunks[1:]
	if chunk != nil && chunk.Usage != nil {
		s.usage = chunk.Usage
	}
	return chunk, nil
}
func (s *fakeProbeStream) Close() error              { s.closed = true; return nil }
func (s *fakeProbeStream) GetUsage() *provider.Usage { return s.usage }

func streamChunk(text, reasoning, finish string) *provider.ChatCompletionChunk {
	return &provider.ChatCompletionChunk{Choices: []provider.Choice{{Delta: &provider.Message{Content: text, ReasoningContent: &reasoning}, FinishReason: &finish}}}
}

func TestProbeStreamBudgetEvidenceUsageAndCleanup(t *testing.T) {
	usage := &llmkit.Usage{TotalTokens: 512, ReasoningTokens: 500}
	tests := []struct {
		name   string
		chunks []*provider.ChatCompletionChunk
		err    error
		want   outcome
		detail string // expected exact detail when the result is INCOMPLETE
	}{
		{"complete", []*provider.ChatCompletionChunk{streamChunk("一", "", ""), streamChunk("二", "", "stop")}, nil, pass, ""},
		{"one text frame", []*provider.ChatCompletionChunk{streamChunk("一二三四五", "", "stop")}, nil, pass, ""},
		{"reasoning then one text frame", []*provider.ChatCompletionChunk{streamChunk("", "thinking", ""), streamChunk("一二三四五", "", "stop")}, nil, pass, ""},
		{"reasoning trace", []*provider.ChatCompletionChunk{streamChunk("", "reasoning", "length")}, nil, incomplete, reasoningBudgetDetail},
		{"reasoning usage", []*provider.ChatCompletionChunk{streamChunk("", "", "length"), {Usage: usage}}, nil, incomplete, reasoningBudgetDetail},
		{"empty length without reasoning", []*provider.ChatCompletionChunk{streamChunk("", "", "length")}, nil, incomplete, outputBudgetDetail},
		{"partial answer", []*provider.ChatCompletionChunk{streamChunk("一", "thinking", ""), streamChunk("二", "", "length")}, nil, incomplete, reasoningBudgetDetail},
		{"partial answer without reasoning", []*provider.ChatCompletionChunk{streamChunk("一", "", ""), streamChunk("二", "", "length")}, nil, incomplete, outputBudgetDetail},
		{"length before usage choice", []*provider.ChatCompletionChunk{streamChunk("", "thinking", "length"), streamChunk("", "", "")}, nil, incomplete, reasoningBudgetDetail},
		{"length before trailing stop", []*provider.ChatCompletionChunk{streamChunk("一二", "thinking", "length"), streamChunk("", "", "stop")}, nil, incomplete, reasoningBudgetDetail},
		{"missing finish", []*provider.ChatCompletionChunk{streamChunk("一", "", ""), streamChunk("二", "", "")}, nil, fail, ""},
		{"stop without any text frame", []*provider.ChatCompletionChunk{{Usage: usage}, streamChunk("", "", "stop")}, nil, fail, ""},
		{"content filter", []*provider.ChatCompletionChunk{streamChunk("一", "", ""), streamChunk("二", "", "content_filter")}, nil, fail, ""},
		{"empty", nil, nil, fail, ""},
		{"whitespace", []*provider.ChatCompletionChunk{streamChunk(" ", "", ""), streamChunk("\n", "", "stop")}, nil, fail, ""},
		{"error", []*provider.ChatCompletionChunk{{Usage: usage}}, errors.New("stream broke"), fail, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := &fakeProbeStream{chunks: tt.chunks, err: tt.err}
			p := &fakeProbeChat{name: llmkit.DeepSeek, stream: stream}
			res := probeStream(context.Background(), probeClient(t, p), "deepseek-flash")
			if res.outcome != tt.want || !stream.closed || len(p.requests) != 1 || p.requests[0].MaxTokens == nil || *p.requests[0].MaxTokens != 512 {
				t.Fatalf("result=%+v closed=%v requests=%+v", res, stream.closed, p.requests)
			}
			if stream.usage != nil && res.usage != stream.usage {
				t.Error("final/error stream usage lost")
			}
			if tt.want == incomplete {
				// The detail must name why the probe is inconclusive, not just say so.
				if !strings.Contains(res.detail, "finish_reason=length") {
					t.Errorf("incomplete detail lacks budget evidence: %q", res.detail)
				}
				if tt.detail != "" && res.detail != tt.detail {
					t.Errorf("detail = %q, want %q", res.detail, tt.detail)
				}
			}
			// The "no chunks" wording has to match what is actually counted: the
			// probe counts text deltas, not events.
			if tt.name == "stop without any text frame" && !strings.Contains(res.detail, "没有收到任何正文 delta") {
				t.Errorf("detail = %q, want it to name text deltas", res.detail)
			}
			// A single-frame answer passes, but the detail must keep the signal:
			// a vendor that buffers the whole reply looks exactly like this.
			if tt.name == "one text frame" || tt.name == "reasoning then one text frame" {
				if !strings.Contains(res.detail, "单帧正文") {
					t.Errorf("single-frame answer is missing the buffering annotation: %q", res.detail)
				}
			}
		})
	}
}

func TestProbeThinkingUsesBudgetAndClassifiesLength(t *testing.T) {
	for _, tt := range []struct {
		name   string
		resp   *llmkit.ChatResponse
		want   outcome
		detail string
	}{
		{"non-reasoning model", probeResponse("15", "", "stop", nil), notApplicable, ""},
		{"length without reasoning", probeResponse("", "", "length", nil), incomplete, reasoningBudgetDetail},
		{"length with reasoning", probeResponse("15", "13 更接近", "length", nil), pass, ""},
		{"reasoning", probeResponse("15", "13 更接近", "stop", nil), pass, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
				return tt.resp, nil
			}}
			res := probeThinking(context.Background(), probeClient(t, p), "deepseek-flash")
			if res.outcome != tt.want {
				t.Fatalf("result=%+v, want %v", res, tt.want)
			}
			if tt.detail != "" && res.detail != tt.detail {
				t.Errorf("detail=%q want=%q", res.detail, tt.detail)
			}
			if len(p.requests) != 1 || p.requests[0].MaxTokens == nil || *p.requests[0].MaxTokens != probeMaxTokens {
				t.Fatalf("thinking probe must use the bounded %d-token budget: requests=%+v", probeMaxTokens, p.requests)
			}
			if p.requests[0].Thinking == nil {
				t.Error("thinking parameter was dropped from the request")
			}
		})
	}
	t.Run("invalid request is not applicable", func(t *testing.T) {
		p := &fakeProbeChat{name: llmkit.DeepSeek, chat: func(int, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
			return nil, &provider.ProviderError{StatusCode: 422, Message: "thinking unsupported"}
		}}
		if res := probeThinking(context.Background(), probeClient(t, p), "deepseek-flash"); res.outcome != notApplicable {
			t.Fatalf("result=%+v, want N/A", res)
		}
	})
}

func TestProbeErrorsUsesProbeOptionsAndSkipsKeyOptional(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("probe sent no credential header")
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer server.Close()

	// The deliberately invalid key must be sent to -base-url (a relay or an
	// offline fake upstream), not to the vendor's public host.
	res := probeErrors(context.Background(), llmkit.DeepSeek, "deepseek-flash", probeOptions{baseURL: server.URL, timeout: 5 * time.Second})
	if res.outcome != pass || !strings.Contains(res.detail, "401") || calls.Load() != 1 {
		t.Fatalf("result=%+v calls=%d, want 401 classified as an auth error", res, calls.Load())
	}

	// Key-optional providers cannot produce an auth rejection, so no request
	// should be made at all.
	for _, name := range []string{llmkit.Ollama, llmkit.VLLM} {
		if res := probeErrors(context.Background(), name, "local-model", probeOptions{baseURL: server.URL}); res.outcome != notApplicable {
			t.Errorf("%s: result=%+v, want N/A", name, res)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("key-optional probes contacted upstream: calls=%d", got)
	}
}

func TestProbeErrorsHonorsTimeoutOption(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	start := time.Now()
	res := probeErrors(context.Background(), llmkit.DeepSeek, "deepseek-flash",
		probeOptions{baseURL: server.URL, timeout: 50 * time.Millisecond})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("error probe ignored -timeout: took %s", elapsed)
	}
	if res.outcome != fail || !strings.Contains(res.detail, "未能捕获上游状态码") {
		t.Fatalf("result=%+v, want a timeout failure that captured no status", res)
	}
}

func TestReportExitCodesAndIncompleteSummary(t *testing.T) {
	for _, tt := range []struct {
		name    string
		reports []report
		want    int
	}{
		{"empty", nil, 0},
		{"pass and intentional skips", []report{{results: []result{{outcome: pass}, {outcome: skipped}, {outcome: notApplicable}}}}, 0},
		{"incomplete", []report{{results: []result{{outcome: incomplete}}}}, 2},
		{"failure overrides incomplete", []report{{results: []result{{outcome: incomplete}}}, {results: []result{{outcome: fail}}}}, 1},
		{"setup overrides incomplete", []report{{results: []result{{outcome: incomplete}}}, {setupErr: errors.New("unreachable")}}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := reportsExitCode(tt.reports); got != tt.want {
				t.Fatalf("exit=%d want=%d", got, tt.want)
			}
		})
	}
	if mark(incomplete) != "INCOMPLETE" {
		t.Fatalf("marker=%q", mark(incomplete))
	}
	rep := report{provider: llmkit.DeepSeek, model: "deepseek-flash", results: []result{{name: "非流式对话", outcome: incomplete, detail: reasoningBudgetDetail}}, tokens: 512}
	output := captureProbeOutput(t, func() { printResult(rep.results[0], false); printSummary(rep); printGrandTotal([]report{rep}) })
	for _, want := range []string{"INCOMPLETE", "0 通过", "1 未完成", "未完成项", "finish_reason=length", "TOKENS"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

func TestIncompleteResultKeepsTableAligned(t *testing.T) {
	output := captureProbeOutput(t, func() {
		printResult(result{name: "非流式对话", outcome: pass, detail: "回复可用"}, false)
		printResult(result{name: "非流式对话", outcome: incomplete, detail: reasoningBudgetDetail}, false)
	})
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected output: %q", output)
	}
	for _, line := range lines {
		if got := displayWidth(line); got != 84 {
			t.Errorf("line width=%d want=84: %q", got, line)
		}
	}
	if strings.Index(lines[0], "非流式对话") != strings.Index(lines[1], "非流式对话") {
		t.Errorf("result labels shifted:\n%s", output)
	}
}

type blockingProbeStream struct {
	ctx    context.Context
	closed bool
}

func (s *blockingProbeStream) Recv() (*provider.ChatCompletionChunk, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}
func (s *blockingProbeStream) Close() error              { s.closed = true; return nil }
func (s *blockingProbeStream) GetUsage() *provider.Usage { return nil }

type blockingStreamProvider struct {
	provider.Provider
	stream *blockingProbeStream
}

func (p *blockingStreamProvider) Name() string { return "blocking-stream" }
func (p *blockingStreamProvider) ChatCompletionStream(ctx context.Context, _, _ string, _ *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	p.stream.ctx = ctx
	return p.stream, nil
}

func TestRunProbeDeadlineBoundsBlockedStreamAndClosesIt(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := &blockingProbeStream{}
	c := probeClient(t, &blockingStreamProvider{stream: stream})
	res := runProbe(parent, 25*time.Millisecond, probe{run: func(ctx context.Context) result { return probeStream(ctx, c, "model") }})
	if res.outcome != fail || !strings.Contains(res.detail, "超时") || !stream.closed || !errors.Is(stream.ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("result=%+v closed=%v stream error=%v", res, stream.closed, stream.ctx.Err())
	}
	if parent.Err() != nil {
		t.Fatalf("stream consumed the parent budget: %v", parent.Err())
	}
}

func TestRunProbeContextsAreIndependentAndCanceled(t *testing.T) {
	parent := context.Background()
	var previous context.Context
	for range 2 {
		res := runProbe(parent, time.Second, probe{run: func(ctx context.Context) result {
			if ctx.Err() != nil {
				t.Fatalf("new probe inherited cancellation: %v", ctx.Err())
			}
			if previous != nil && !errors.Is(previous.Err(), context.Canceled) {
				t.Fatal("previous context was not released")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("probe is missing its deadline")
			}
			previous = ctx
			return result{outcome: pass}
		}})
		if res.outcome != pass || !errors.Is(previous.Err(), context.Canceled) {
			t.Fatal("probe did not finish and cancel")
		}
	}
	for _, timeout := range []time.Duration{0, -time.Second} {
		runProbe(parent, timeout, probe{run: func(ctx context.Context) result {
			if _, ok := ctx.Deadline(); ok {
				t.Fatalf("timeout=%s unexpectedly installed a deadline", timeout)
			}
			previous = ctx
			return result{outcome: pass}
		}})
		if !errors.Is(previous.Err(), context.Canceled) {
			t.Fatal("unbounded probe context was not released")
		}
	}
	if parent.Err() != nil {
		t.Fatal("canceling a probe canceled its parent")
	}
}

func TestStreamProbeEntryCancelsHTTPBodyRead(t *testing.T) {
	handlerDone, stopHandler := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume the request so net/http can observe the client's closed
		// connection while this handler waits for stream cancellation.
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(handlerDone)
		case <-stopHandler:
		}
	}))
	defer server.Close()
	defer close(stopHandler)
	client, err := llmkit.New(llmkit.DeepSeek, llmkit.WithAPIKey("offline-key"), llmkit.WithBaseURL(server.URL), llmkit.WithTimeout(0), llmkit.WithRetry(llmkit.NoRetry()))
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var streamProbe probe
	for _, p := range buildProbes(client, "deepseek-flash", target{provider: llmkit.DeepSeek}, probeOptions{timeout: 100 * time.Millisecond}) {
		if p.name == "流式对话" {
			streamProbe = p
			break
		}
	}
	if streamProbe.run == nil {
		t.Fatal("stream probe missing from real probe list")
	}
	res := streamProbe.run(parent)
	if res.outcome != fail || !strings.Contains(res.detail, "超时") {
		t.Fatalf("result=%+v", res)
	}
	if parent.Err() != nil {
		t.Fatalf("stream used the parent deadline: %v", parent.Err())
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not observe request cancellation")
	}
}

func captureProbeOutput(t *testing.T, f func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	old := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = old }()
	f()
	writer.Close()
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}
