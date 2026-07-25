package safehttp

import (
	"errors"
	"net"
	"testing"
)

// TestIsBlockedIP 覆盖 §14.3 黑名单：
//
//	loopback / link-local / private (RFC1918) / IPv6 ULA / multicast / unspecified /
//	cloud metadata (169.254.169.254 落在 link-local) / CGN
func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		// blocked
		{"127.0.0.1", true},
		{"127.0.0.53", true},
		{"::1", true},
		{"169.254.169.254", true}, // AWS / GCP / Azure metadata
		{"169.254.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"fc00::1", true},    // IPv6 ULA
		{"fe80::1", true},    // IPv6 link-local
		{"0.0.0.0", true},    // unspecified
		{"::", true},         // IPv6 unspecified
		{"224.0.0.1", true},  // IPv4 multicast
		{"ff02::1", true},    // IPv6 multicast
		{"100.64.0.1", true}, // CGN
		{"100.127.255.255", true},

		// allowed (公网)
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"172.15.255.255", false}, // 紧贴 private 边界外
		{"172.32.0.1", false},
		{"100.63.255.255", false}, // 紧贴 CGN 边界外
		{"100.128.0.1", false},
		{"2606:4700:4700::1111", false}, // Cloudflare DNS IPv6
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("ParseIP: bad test IP %q", tc.ip)
			}
			if got := isBlockedIP(ip); got != tc.want {
				t.Errorf("isBlockedIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

// TestValidateImageBytes 覆盖 §14.2 MIME 双重校验：
//   - 嗅探类型不在白名单（svg）→ ErrUnsupportedMime
//   - 嗅探类型与 declared content-type 不一致 → ErrMimeDeclaredMismatch
//   - 一致且在白名单 → 通过
func TestValidateImageBytes(t *testing.T) {
	cli := NewSafeClient(DefaultImageConfig())

	// PNG magic bytes
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	pngBuf := append(pngMagic, make([]byte, 512)...)

	// JPEG magic
	jpegBuf := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 512)...)

	// SVG (XML) — http.DetectContentType 把 <?xml...><svg... 嗅成 image/svg+xml 或 text/xml
	svgBuf := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"></svg>`)

	t.Run("png declared matches", func(t *testing.T) {
		mime, err := cli.ValidateImageBytes(pngBuf, "image/png")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/png" {
			t.Errorf("got mime %q, want image/png", mime)
		}
	})

	t.Run("jpeg no declared CT", func(t *testing.T) {
		mime, err := cli.ValidateImageBytes(jpegBuf, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/jpeg" {
			t.Errorf("got mime %q, want image/jpeg", mime)
		}
	})

	t.Run("png declared as jpeg should fail", func(t *testing.T) {
		_, err := cli.ValidateImageBytes(pngBuf, "image/jpeg")
		if !errors.Is(err, ErrMimeDeclaredMismatch) {
			t.Errorf("expected ErrMimeDeclaredMismatch, got %v", err)
		}
	})

	t.Run("svg rejected by whitelist", func(t *testing.T) {
		_, err := cli.ValidateImageBytes(svgBuf, "")
		if !errors.Is(err, ErrUnsupportedMime) {
			t.Errorf("expected ErrUnsupportedMime, got %v", err)
		}
	})

	t.Run("png with charset in declared", func(t *testing.T) {
		// http 头里 "image/png; charset=binary" 应该被解析掉 charset
		mime, err := cli.ValidateImageBytes(pngBuf, "image/png; charset=binary")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mime != "image/png" {
			t.Errorf("got mime %q, want image/png", mime)
		}
	})
}

func TestSchemeAllowed(t *testing.T) {
	if !schemeAllowed("https", []string{"https"}) {
		t.Errorf("https should be allowed")
	}
	if schemeAllowed("http", []string{"https"}) {
		t.Errorf("http should be rejected by default")
	}
	if schemeAllowed("file", []string{"https", "http"}) {
		t.Errorf("file scheme should not slip through")
	}
}
