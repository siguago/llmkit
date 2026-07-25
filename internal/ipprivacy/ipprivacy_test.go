package ipprivacy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStripClientIPHeaders(t *testing.T) {
	h := http.Header{}
	for _, name := range ClientIPHeaders {
		h.Set(name, "203.0.113.10")
	}
	h.Set("Authorization", "Bearer provider-key")
	h.Set("Content-Type", "application/json")

	StripClientIPHeaders(h)

	for _, name := range ClientIPHeaders {
		if got := h.Get(name); got != "" {
			t.Fatalf("%s was not stripped: %q", name, got)
		}
	}
	if got := h.Get("Authorization"); got != "Bearer provider-key" {
		t.Fatalf("Authorization should be preserved, got %q", got)
	}
	if got := h.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type should be preserved, got %q", got)
	}
}

func TestTransportStripsClientIPHeadersBeforeRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range ClientIPHeaders {
			if got := r.Header.Get(name); got != "" {
				t.Fatalf("upstream received %s=%q", name, got)
			}
		}
		if got := r.Header.Get("Authorization"); got != "Bearer provider-key" {
			t.Fatalf("Authorization should be preserved, got %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	client := &http.Client{Transport: NewTransport(http.DefaultTransport)}
	req, err := http.NewRequest(http.MethodGet, upstream.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range ClientIPHeaders {
		req.Header.Set(name, "203.0.113.10")
	}
	req.Header.Set("Authorization", "Bearer provider-key")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
