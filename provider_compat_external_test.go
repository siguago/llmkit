package llmkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/siguago/llmkit"
	responsesapi "github.com/siguago/llmkit/protocol/responses"
	"github.com/siguago/llmkit/provider"
)

// legacyV03Provider deliberately implements only the pre-native Provider
// contract. Keeping this test in an external package proves that adding native
// protocol support did not require third-party adapters to grow new methods.
type legacyV03Provider struct{}

var _ provider.Provider = (*legacyV03Provider)(nil)

func (*legacyV03Provider) Name() string { return "legacy-v0.3" }

func (*legacyV03Provider) ChatCompletion(
	context.Context,
	string,
	string,
	*provider.ChatCompletionRequest,
) (*provider.ChatCompletionResponse, error) {
	return &provider.ChatCompletionResponse{}, nil
}

func (*legacyV03Provider) ChatCompletionStream(
	context.Context,
	string,
	string,
	*provider.ChatCompletionRequest,
) (provider.StreamReader, error) {
	return nil, errors.New("not used")
}

func TestLegacyProviderContractRemainsSourceCompatible(t *testing.T) {
	client, err := llmkit.Wrap(&legacyV03Provider{}, llmkit.WithAPIKey("test-key"))
	if err != nil {
		t.Fatalf("Wrap legacy provider: %v", err)
	}
	if client.SupportsResponses() || client.SupportsAnthropicMessages() {
		t.Fatal("legacy provider unexpectedly advertises a native protocol")
	}
	_, err = client.CreateResponse(context.Background(), &responsesapi.CreateRequest{})
	if !errors.Is(err, llmkit.ErrUnsupported) {
		t.Fatalf("CreateResponse error = %v, want ErrUnsupported", err)
	}
}
