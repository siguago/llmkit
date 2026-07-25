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
