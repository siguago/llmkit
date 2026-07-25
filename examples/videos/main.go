// Command videos submits an asynchronous video job and waits for it.
//
//	GEMINI_API_KEY=... go run ./examples/videos "海浪拍打礁石的慢镜头"
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/siguago/llmkit"
)

func main() {
	prompt := "海浪拍打礁石的慢镜头，黄昏光线"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	client, err := llmkit.New(llmkit.Gemini)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	duration := 8.0
	job, err := client.CreateVideo(ctx, &llmkit.VideoRequest{
		Model:           "veo-3.1-generate-preview",
		Prompt:          prompt,
		DurationSeconds: &duration,
		AspectRatio:     "16:9",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("job %s submitted (status %s)\n", job.ProviderJobID, job.Status)

	// WaitVideo polls until the job reaches a terminal state.
	job, err = client.WaitVideo(ctx, job, &llmkit.WaitOptions{
		Interval: 10 * time.Second,
		Timeout:  15 * time.Minute,
		OnUpdate: func(j *llmkit.VideoJob) {
			fmt.Printf("  status=%s progress=%d%%\n", j.Status, j.Progress)
		},
	})
	if err != nil {
		log.Fatalf("waiting: %v", err)
	}

	// A finished-but-failed job comes back with a nil error — the wait
	// succeeded, the generation did not.
	if job.Status != llmkit.VideoStatusCompleted {
		log.Fatalf("job ended as %s: %+v", job.Status, job.Error)
	}

	for i, asset := range job.Assets {
		fmt.Printf("asset %d: %s (%dms, %s)\n", i, asset.URL, asset.DurationMs, asset.MimeType)
	}
	if u := job.Usage; u != nil {
		fmt.Printf("duration_ms=%d cost=%.4f\n", u.DurationMs, u.Cost)
	}
}
