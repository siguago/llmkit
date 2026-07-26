//go:build integration

// This test makes a REAL API call. The env-var guard alone already kept it from
// running by default, but the build tag is what the rest of the repo uses to
// mark live tests, and it keeps `go test ./...` from even compiling a path to
// the network. Run it with -tags=integration.
package easyrouter

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveListModels(t *testing.T) {
	apiKey := os.Getenv("EASYROUTER_API_KEY")
	if apiKey == "" {
		t.Skip("set EASYROUTER_API_KEY to run live EasyRouter smoke test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	models, err := New(os.Getenv("EASYROUTER_BASE_URL")).ListModels(ctx, apiKey)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("ListModels returned no models")
	}
}
