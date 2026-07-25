package llmkit

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/siguago/llmkit/provider"
)

// fakeVideoProvider is a provider.Provider + provider.VideoProvider whose
// GetVideoJob answers from a scripted sequence, so WaitVideo's polling loop can
// be driven deterministically without a network.
type fakeVideoProvider struct {
	name string

	// script is consumed one entry per GetVideoJob call; the last entry repeats.
	script []videoStep
	calls  atomic.Int32

	cancelErr error
	cancelled atomic.Bool
}

type videoStep struct {
	status string
	err    error
}

func (f *fakeVideoProvider) Name() string { return f.name }

func (f *fakeVideoProvider) ChatCompletion(context.Context, string, string, *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeVideoProvider) ChatCompletionStream(context.Context, string, string, *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeVideoProvider) CreateVideoJob(_ context.Context, _, model string, req *provider.VideoCreateRequest) (*provider.VideoJob, error) {
	return &provider.VideoJob{
		ID:            "job-1",
		ProviderJobID: "up-1",
		Model:         model,
		ProviderName:  f.name,
		Status:        provider.VideoStatusQueued,
	}, nil
}

func (f *fakeVideoProvider) GetVideoJob(_ context.Context, _ string, job *provider.VideoJob) (*provider.VideoJob, error) {
	i := int(f.calls.Add(1)) - 1
	if i >= len(f.script) {
		i = len(f.script) - 1
	}
	step := f.script[i]
	if step.err != nil {
		return nil, step.err
	}
	out := *job
	out.Status = step.status
	if step.status == provider.VideoStatusCompleted {
		out.Assets = []provider.MediaAsset{{Type: "video", URL: "https://cdn.example/v.mp4"}}
	}
	return &out, nil
}

func (f *fakeVideoProvider) CancelVideoJob(_ context.Context, _ string, job *provider.VideoJob) (*provider.VideoJob, error) {
	if f.cancelErr != nil {
		return nil, f.cancelErr
	}
	f.cancelled.Store(true)
	out := *job
	out.Status = provider.VideoStatusCancelled
	return &out, nil
}

func newFakeVideoClient(t *testing.T, f *fakeVideoProvider) *Client {
	t.Helper()
	c, err := Wrap(f, WithAPIKey("sk-test"), WithRetry(NoRetry()))
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	return c
}

func TestWaitVideo_PollsUntilCompleted(t *testing.T) {
	f := &fakeVideoProvider{name: "fakevideo", script: []videoStep{
		{status: provider.VideoStatusQueued},
		{status: provider.VideoStatusInProgress},
		{status: provider.VideoStatusCompleted},
	}}
	c := newFakeVideoClient(t, f)

	job, err := c.CreateVideo(context.Background(), &VideoRequest{Model: "veo", Prompt: "x"})
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}

	var seen []string
	job, err = c.WaitVideo(context.Background(), job, &WaitOptions{
		Interval: time.Millisecond,
		Timeout:  5 * time.Second,
		OnUpdate: func(j *VideoJob) { seen = append(seen, j.Status) },
	})
	if err != nil {
		t.Fatalf("WaitVideo: %v", err)
	}
	if job.Status != VideoStatusCompleted {
		t.Errorf("status = %q, want completed", job.Status)
	}
	if len(job.Assets) != 1 {
		t.Errorf("assets = %+v", job.Assets)
	}
	if len(seen) != 3 {
		t.Errorf("OnUpdate fired %d times (%v), want 3", len(seen), seen)
	}
}

// A job that finishes as failed must come back with a NIL error: the wait
// succeeded, the generation didn't. Callers check job.Status.
func TestWaitVideo_FailedJobIsNotAnError(t *testing.T) {
	f := &fakeVideoProvider{name: "fakevideo", script: []videoStep{
		{status: provider.VideoStatusFailed},
	}}
	c := newFakeVideoClient(t, f)

	job, err := c.WaitVideo(context.Background(),
		&VideoJob{ID: "job-1", ProviderJobID: "up-1", Status: VideoStatusQueued},
		&WaitOptions{Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("WaitVideo returned an error for a failed job: %v", err)
	}
	if job.Status != VideoStatusFailed {
		t.Errorf("status = %q, want failed", job.Status)
	}
}

// An already-terminal job must return immediately without polling at all.
func TestWaitVideo_ReturnsImmediatelyWhenAlreadyTerminal(t *testing.T) {
	f := &fakeVideoProvider{name: "fakevideo", script: []videoStep{
		{status: provider.VideoStatusInProgress},
	}}
	c := newFakeVideoClient(t, f)

	start := time.Now()
	job, err := c.WaitVideo(context.Background(),
		&VideoJob{ID: "job-1", Status: VideoStatusCompleted},
		&WaitOptions{Interval: time.Hour})
	if err != nil {
		t.Fatalf("WaitVideo: %v", err)
	}
	if job.Status != VideoStatusCompleted {
		t.Errorf("status = %q", job.Status)
	}
	if f.calls.Load() != 0 {
		t.Errorf("polled %d times for an already-terminal job, want 0", f.calls.Load())
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v; should return immediately", elapsed)
	}
}

// Transient poll failures must not abandon a job that may still be running.
func TestWaitVideo_SurvivesTransientPollFailures(t *testing.T) {
	f := &fakeVideoProvider{name: "fakevideo", script: []videoStep{
		{err: &APIError{StatusCode: http.StatusServiceUnavailable, Message: "blip"}},
		{err: &APIError{StatusCode: http.StatusNotFound, Message: "not indexed yet"}},
		{status: provider.VideoStatusCompleted},
	}}
	c := newFakeVideoClient(t, f)

	job, err := c.WaitVideo(context.Background(),
		&VideoJob{ID: "job-1", Status: VideoStatusQueued},
		&WaitOptions{Interval: time.Millisecond, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("WaitVideo: %v", err)
	}
	if job.Status != VideoStatusCompleted {
		t.Errorf("status = %q, want completed", job.Status)
	}
}

// A permanent failure (bad credential) must abort the wait rather than spin
// until the deadline.
func TestWaitVideo_AbortsOnPermanentError(t *testing.T) {
	f := &fakeVideoProvider{name: "fakevideo", script: []videoStep{
		{err: &APIError{StatusCode: http.StatusUnauthorized, Message: "bad key"}},
	}}
	c := newFakeVideoClient(t, f)

	start := time.Now()
	_, err := c.WaitVideo(context.Background(),
		&VideoJob{ID: "job-1", Status: VideoStatusQueued},
		&WaitOptions{Interval: time.Millisecond, Timeout: 30 * time.Second})
	if !IsAuthError(err) {
		t.Fatalf("err = %v, want the 401 surfaced", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v; a 401 should abort immediately", elapsed)
	}
}

func TestWaitVideo_TimesOut(t *testing.T) {
	f := &fakeVideoProvider{name: "fakevideo", script: []videoStep{
		{status: provider.VideoStatusInProgress},
	}}
	c := newFakeVideoClient(t, f)

	job, err := c.WaitVideo(context.Background(),
		&VideoJob{ID: "job-1", Status: VideoStatusQueued},
		&WaitOptions{Interval: time.Millisecond, Timeout: 50 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	// The last known job state must still come back so callers can report it.
	if job == nil || job.ID != "job-1" {
		t.Errorf("job = %+v, want the last known state", job)
	}
}

func TestWaitVideo_HonorsCallerCancellation(t *testing.T) {
	f := &fakeVideoProvider{name: "fakevideo", script: []videoStep{
		{status: provider.VideoStatusInProgress},
	}}
	c := newFakeVideoClient(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := c.WaitVideo(ctx, &VideoJob{ID: "job-1", Status: VideoStatusQueued},
			&WaitOptions{Interval: time.Millisecond, Timeout: time.Hour})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitVideo ignored caller cancellation")
	}
}

func TestWaitVideo_DefaultsAreApplied(t *testing.T) {
	f := &fakeVideoProvider{name: "fakevideo", script: []videoStep{
		{status: provider.VideoStatusCompleted},
	}}
	c := newFakeVideoClient(t, f)

	// nil options must not panic and must use the 5s/30m defaults. The first
	// tick lands at 5s, so bound the test with our own context instead.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := c.WaitVideo(ctx, &VideoJob{ID: "job-1", Status: VideoStatusQueued}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v; with default 5s interval the 100ms ctx should expire first", err)
	}
}

func TestVideoLifecycle_GetAndCancel(t *testing.T) {
	f := &fakeVideoProvider{name: "fakevideo", script: []videoStep{
		{status: provider.VideoStatusInProgress},
	}}
	c := newFakeVideoClient(t, f)
	ctx := context.Background()

	job, err := c.CreateVideo(ctx, &VideoRequest{Model: "veo", Prompt: "x"})
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}

	got, err := c.GetVideo(ctx, job)
	if err != nil {
		t.Fatalf("GetVideo: %v", err)
	}
	if got.Status != VideoStatusInProgress {
		t.Errorf("status = %q", got.Status)
	}

	cancelled, err := c.CancelVideo(ctx, job)
	if err != nil {
		t.Fatalf("CancelVideo: %v", err)
	}
	if cancelled.Status != VideoStatusCancelled || !f.cancelled.Load() {
		t.Errorf("cancel did not take effect: %+v", cancelled)
	}
}

// A vendor with no cancel endpoint reports provider.ErrUnsupported; the façade
// must translate it into the SDK's own ErrUnsupported.
func TestCancelVideo_TranslatesAdapterUnsupported(t *testing.T) {
	f := &fakeVideoProvider{
		name:      "fakevideo",
		script:    []videoStep{{status: provider.VideoStatusInProgress}},
		cancelErr: &provider.ErrUnsupported{Provider: "fakevideo", Op: "cancel_video_job"},
	}
	c := newFakeVideoClient(t, f)

	_, err := c.CancelVideo(context.Background(), &VideoJob{ID: "job-1"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if !IsUnsupportedCapability(err) {
		t.Error("IsUnsupportedCapability should report true")
	}
}

func TestVideoMethods_RejectNilJob(t *testing.T) {
	c := newFakeVideoClient(t, &fakeVideoProvider{name: "fakevideo", script: []videoStep{{status: "queued"}}})
	ctx := context.Background()

	if _, err := c.GetVideo(ctx, nil); err == nil {
		t.Error("GetVideo(nil) should error")
	}
	if _, err := c.CancelVideo(ctx, nil); err == nil {
		t.Error("CancelVideo(nil) should error")
	}
	if _, err := c.WaitVideo(ctx, nil, nil); err == nil {
		t.Error("WaitVideo(nil) should error")
	}
}
