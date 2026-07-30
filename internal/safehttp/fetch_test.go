package safehttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// pngPayload returns bytes http.DetectContentType sniffs as image/png. The
// padding matters: DetectContentType reads up to 512 bytes.
func pngPayload(pad int) []byte {
	return append([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, pad)...)
}

// stubbed swaps the transport so a test can drive FetchImage's post-dial logic
// without a real connection. The dialer's IP check is deliberately bypassed
// here — TestFetchImage_DialerBlocksLoopback covers it against a live listener.
func stubbed(s *SafeClient, fn roundTripFunc) *SafeClient {
	s.client.Transport = fn
	return s
}

func respond(status int, contentType string, body []byte, contentLength int64) *http.Response {
	h := make(http.Header)
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode:    status,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: contentLength,
	}
}

// A zero Config must not produce a client with no timeouts and no redirect
// ceiling — those zero values would each be a hang or an open redirect chain.
func TestNewSafeClient_FillsDefaults(t *testing.T) {
	s := NewSafeClient(Config{})

	if s.cfg.DialTimeout != 10*time.Second {
		t.Errorf("DialTimeout = %v, want 10s", s.cfg.DialTimeout)
	}
	if s.cfg.RequestTimeout != 30*time.Second {
		t.Errorf("RequestTimeout = %v, want 30s", s.cfg.RequestTimeout)
	}
	if s.cfg.MaxRedirects != 3 {
		t.Errorf("MaxRedirects = %d, want 3", s.cfg.MaxRedirects)
	}
	if s.client.Timeout != 30*time.Second {
		t.Errorf("client.Timeout = %v, want 30s", s.client.Timeout)
	}
}

// Explicit values must survive the defaulting pass.
func TestNewSafeClient_KeepsExplicitValues(t *testing.T) {
	s := NewSafeClient(Config{
		DialTimeout:    time.Second,
		RequestTimeout: 2 * time.Second,
		MaxRedirects:   7,
	})
	if s.cfg.DialTimeout != time.Second || s.cfg.RequestTimeout != 2*time.Second || s.cfg.MaxRedirects != 7 {
		t.Errorf("explicit config was overwritten: %+v", s.cfg)
	}
}

func TestDefaultImageConfig(t *testing.T) {
	cfg := DefaultImageConfig()

	if cfg.MaxBytes != 20*1024*1024 {
		t.Errorf("MaxBytes = %d, want 20MiB", cfg.MaxBytes)
	}
	if len(cfg.AllowedSchemes) != 1 || cfg.AllowedSchemes[0] != "https" {
		t.Errorf("default schemes = %v, want [https] only", cfg.AllowedSchemes)
	}
	// SVG is XML: it can carry script and external entities. Its absence here
	// is the whole reason ValidateImageBytes whitelists rather than blacklists.
	for _, mime := range cfg.AllowedImageMimes {
		if strings.Contains(mime, "svg") {
			t.Errorf("svg must not be in the default whitelist: %v", cfg.AllowedImageMimes)
		}
	}
}

// The core SSRF assertion, and the one path the package existed without a test
// for: even with the scheme restriction lifted, a URL resolving to a loopback
// address must die in Dialer.Control before any bytes are exchanged.
//
// This is what makes DNS rebinding survivable — the check runs after resolution,
// on the address actually being connected to.
func TestFetchImage_DialerBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached the server; the dialer should have blocked it")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := DefaultImageConfig()
	cfg.AllowedSchemes = []string{"http", "https"} // lift the scheme gate to reach the dialer
	s := NewSafeClient(cfg)

	_, _, err := s.FetchImage(context.Background(), srv.URL)
	if !errors.Is(err, ErrBlockedIP) {
		t.Fatalf("loopback target should fail with ErrBlockedIP, got %v", err)
	}
}

func TestFetchImage_RejectsDisallowedSchemes(t *testing.T) {
	s := NewSafeClient(DefaultImageConfig())

	for _, url := range []string{
		"http://example.com/a.png",
		"file:///etc/passwd",
		"gopher://example.com/",
		"ftp://example.com/a.png",
		"//example.com/a.png",
		"",
	} {
		t.Run(url, func(t *testing.T) {
			if _, _, err := s.FetchImage(context.Background(), url); !errors.Is(err, ErrSchemeNotAllowed) {
				t.Errorf("FetchImage(%q) = %v, want ErrSchemeNotAllowed", url, err)
			}
		})
	}
}

func TestFetchImage_Success(t *testing.T) {
	payload := pngPayload(600)
	s := stubbed(NewSafeClient(DefaultImageConfig()), func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		return respond(http.StatusOK, "image/png", payload, int64(len(payload))), nil
	})

	buf, mime, err := s.FetchImage(context.Background(), "https://example.com/a.png")
	if err != nil {
		t.Fatalf("FetchImage: %v", err)
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
	if !bytes.Equal(buf, payload) {
		t.Errorf("body not returned intact: got %d bytes, want %d", len(buf), len(payload))
	}
}

func TestFetchImage_UpstreamErrorStatus(t *testing.T) {
	s := stubbed(NewSafeClient(DefaultImageConfig()), func(*http.Request) (*http.Response, error) {
		return respond(http.StatusNotFound, "text/plain", []byte("nope"), 4), nil
	})

	_, _, err := s.FetchImage(context.Background(), "https://example.com/a.png")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected an error naming the upstream status, got %v", err)
	}
}

// A declared Content-Length over the ceiling is rejected before the body is
// read at all — the cheap check that keeps a hostile server from streaming
// 20MB into memory just to be discarded.
func TestFetchImage_SizeExceededByContentLength(t *testing.T) {
	cfg := DefaultImageConfig()
	cfg.MaxBytes = 1024
	s := stubbed(NewSafeClient(cfg), func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, "image/png", pngPayload(16), 4096), nil
	})

	if _, _, err := s.FetchImage(context.Background(), "https://example.com/a.png"); !errors.Is(err, ErrSizeExceeded) {
		t.Fatalf("expected ErrSizeExceeded, got %v", err)
	}
}

// A server that lies — or just uses chunked encoding, where ContentLength is
// -1 — must still be capped. This is the LimitReader(MaxBytes+1) path.
func TestFetchImage_SizeExceededByBody(t *testing.T) {
	cfg := DefaultImageConfig()
	cfg.MaxBytes = 512
	s := stubbed(NewSafeClient(cfg), func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, "image/png", pngPayload(4096), -1), nil // unknown length
	})

	if _, _, err := s.FetchImage(context.Background(), "https://example.com/a.png"); !errors.Is(err, ErrSizeExceeded) {
		t.Fatalf("expected ErrSizeExceeded, got %v", err)
	}
}

// Exactly at the ceiling is allowed; the +1 on the LimitReader exists so that
// "read one more than the limit" distinguishes at-limit from over-limit.
func TestFetchImage_SizeAtLimitAllowed(t *testing.T) {
	payload := pngPayload(600)
	cfg := DefaultImageConfig()
	cfg.MaxBytes = int64(len(payload))
	s := stubbed(NewSafeClient(cfg), func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, "image/png", payload, -1), nil
	})

	if _, _, err := s.FetchImage(context.Background(), "https://example.com/a.png"); err != nil {
		t.Fatalf("a body exactly at MaxBytes should be accepted, got %v", err)
	}
}

// A JPEG served as image/png is the Content-Type forgery case: the sniffed
// bytes win, and the mismatch is surfaced rather than silently trusted.
func TestFetchImage_DeclaredMimeMismatch(t *testing.T) {
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 600)...)
	s := stubbed(NewSafeClient(DefaultImageConfig()), func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, "image/png", jpeg, int64(len(jpeg))), nil
	})

	if _, _, err := s.FetchImage(context.Background(), "https://example.com/a.png"); !errors.Is(err, ErrMimeDeclaredMismatch) {
		t.Fatalf("expected ErrMimeDeclaredMismatch, got %v", err)
	}
}

func TestFetchImage_SvgRejected(t *testing.T) {
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	s := stubbed(NewSafeClient(DefaultImageConfig()), func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, "image/svg+xml", svg, int64(len(svg))), nil
	})

	if _, _, err := s.FetchImage(context.Background(), "https://example.com/a.svg"); !errors.Is(err, ErrUnsupportedMime) {
		t.Fatalf("expected ErrUnsupportedMime, got %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestFetchImage_BodyReadError(t *testing.T) {
	s := stubbed(NewSafeClient(DefaultImageConfig()), func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"image/png"}},
			Body:          io.NopCloser(errReader{}),
			ContentLength: 100,
		}, nil
	})

	if _, _, err := s.FetchImage(context.Background(), "https://example.com/a.png"); err == nil {
		t.Fatal("a mid-body read failure should surface, not yield a truncated image")
	}
}

// Passes the scheme prefix check but fails to parse as a URL — the branch
// between validateScheme and the transport.
func TestFetchImage_MalformedURL(t *testing.T) {
	s := stubbed(NewSafeClient(DefaultImageConfig()), func(*http.Request) (*http.Response, error) {
		t.Error("a malformed URL should never reach the transport")
		return nil, errors.New("unreachable")
	})

	if _, _, err := s.FetchImage(context.Background(), "https://exa mple.com/a.png"); err == nil {
		t.Fatal("expected a request-construction error")
	}
}

func TestFetchImage_TransportError(t *testing.T) {
	s := stubbed(NewSafeClient(DefaultImageConfig()), func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: no route to host")
	})

	if _, _, err := s.FetchImage(context.Background(), "https://example.com/a.png"); err == nil {
		t.Fatal("expected the transport error to surface")
	}
}

// Redirect chains are how an allowed host hands you off to a blocked one, so
// the hop count is capped independently of the per-hop dial check.
func TestFetchImage_TooManyRedirects(t *testing.T) {
	cfg := DefaultImageConfig()
	cfg.MaxRedirects = 2
	s := stubbed(NewSafeClient(cfg), func(r *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Location", "https://example.com/next")
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     h,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Request:    r,
		}, nil
	})

	if _, _, err := s.FetchImage(context.Background(), "https://example.com/a.png"); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("expected ErrTooManyRedirects, got %v", err)
	}
}

// The scheme gate is re-applied on every hop: an https entry point must not be
// able to redirect down to http, where the response could be tampered with.
func TestFetchImage_RedirectToDisallowedScheme(t *testing.T) {
	s := stubbed(NewSafeClient(DefaultImageConfig()), func(r *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Location", "http://example.com/downgraded")
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     h,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Request:    r,
		}, nil
	})

	if _, _, err := s.FetchImage(context.Background(), "https://example.com/a.png"); !errors.Is(err, ErrSchemeNotAllowed) {
		t.Fatalf("expected ErrSchemeNotAllowed on the redirect hop, got %v", err)
	}
}

// A redirect within the allowance is followed normally.
func TestFetchImage_FollowsAllowedRedirect(t *testing.T) {
	payload := pngPayload(600)
	var hops int
	s := stubbed(NewSafeClient(DefaultImageConfig()), func(r *http.Request) (*http.Response, error) {
		hops++
		if hops == 1 {
			h := make(http.Header)
			h.Set("Location", "https://cdn.example.com/a.png")
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     h,
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Request:    r,
			}, nil
		}
		return respond(http.StatusOK, "image/png", payload, int64(len(payload))), nil
	})

	_, mime, err := s.FetchImage(context.Background(), "https://example.com/a.png")
	if err != nil {
		t.Fatalf("a single redirect should be followed: %v", err)
	}
	if mime != "image/png" || hops != 2 {
		t.Errorf("mime = %q after %d hops, want image/png after 2", mime, hops)
	}
}

func TestFetchImage_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := stubbed(NewSafeClient(DefaultImageConfig()), func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})

	if _, _, err := s.FetchImage(ctx, "https://example.com/a.png"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// An empty whitelist is the documented opt-out: no sniffing, no verdict.
func TestValidateImageBytes_EmptyWhitelistSkipsCheck(t *testing.T) {
	cfg := DefaultImageConfig()
	cfg.AllowedImageMimes = nil
	s := NewSafeClient(cfg)

	mime, err := s.ValidateImageBytes([]byte("anything at all"), "text/html")
	if err != nil {
		t.Fatalf("an empty whitelist should skip validation entirely, got %v", err)
	}
	if mime != "" {
		t.Errorf("skipped validation should report no authoritative mime, got %q", mime)
	}
}

// The sniffed type is returned alongside the error so callers can log what the
// bytes actually were, not just that they were wrong.
func TestValidateImageBytes_ReturnsSniffedMimeOnRejection(t *testing.T) {
	s := NewSafeClient(DefaultImageConfig())

	sniffed, err := s.ValidateImageBytes([]byte("plain text, definitely not an image"), "")
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if sniffed == "" {
		t.Error("the sniffed mime should accompany the rejection")
	}
}

// Empty input sniffs to text/plain, so it is rejected by the whitelist rather
// than slipping through as a zero-length "valid" image.
func TestValidateImageBytes_EmptyInput(t *testing.T) {
	s := NewSafeClient(DefaultImageConfig())

	if _, err := s.ValidateImageBytes(nil, ""); !errors.Is(err, ErrUnsupportedMime) {
		t.Fatalf("empty input should be rejected, got %v", err)
	}
}

// isBlockedIP is the last line of defence and is called with whatever
// SplitHostPort produced; an unparseable address must fail closed.
func TestIsBlockedIP_NilFailsClosed(t *testing.T) {
	if !isBlockedIP(nil) {
		t.Error("a nil IP must be treated as blocked, not allowed")
	}
}

func TestMimeAllowed(t *testing.T) {
	allowed := []string{"image/png", "image/jpeg"}

	if !mimeAllowed("image/png", allowed) {
		t.Error("image/png should be allowed")
	}
	if mimeAllowed("image/svg+xml", allowed) {
		t.Error("image/svg+xml should not be allowed")
	}
	if mimeAllowed("image/png", nil) {
		t.Error("nothing is allowed by an empty list")
	}
}
