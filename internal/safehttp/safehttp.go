// Package safehttp 提供 SSRF 安全的 HTTP 下载与图片 MIME 双重校验工具。
//
// 设计 §14.2 / §14.3:
//   - 用 net.Dialer.Control 在 TCP Dial 阶段校验解析到的 IP，防 DNS rebinding
//   - 拒绝 private / loopback / link-local / multicast / unspecified / IPv6 ULA
//   - 限制重定向次数（每跳重新走 Dial 控制）
//   - 限制响应字节数
//   - 默认仅允许 https://
//   - 拒绝 svg / 未知 MIME；用 http.DetectContentType 双重校验声明的 Content-Type
//
// 不依赖任何项目内部包，可独立单测。
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/siguago/llmkit/internal/ipprivacy"
)

// Errors returned by safehttp.
var (
	ErrSchemeNotAllowed     = errors.New("safehttp: scheme not allowed (only https)")
	ErrBlockedIP            = errors.New("safehttp: target IP is blocked (private/loopback/link-local)")
	ErrSizeExceeded         = errors.New("safehttp: response exceeds size limit")
	ErrTooManyRedirects     = errors.New("safehttp: too many redirects")
	ErrUnsupportedMime      = errors.New("safehttp: unsupported mime type")
	ErrMimeDeclaredMismatch = errors.New("safehttp: declared content-type does not match sniffed bytes")
)

// Config 控制单个 SafeClient 的安全参数。
type Config struct {
	// MaxBytes 单次下载最大字节数。超过即中断并返 ErrSizeExceeded。
	MaxBytes int64
	// MaxRedirects 最大重定向跳数（不含首次请求）。
	MaxRedirects int
	// AllowedSchemes 允许的 URL scheme；默认 ["https"]，调试时可放开 "http"。
	AllowedSchemes []string
	// AllowedImageMimes 允许的输入图片 MIME；默认 image/png, image/jpeg, image/webp。
	// 留空表示 SkipMimeCheck。
	AllowedImageMimes []string
	// DialTimeout TCP 连接超时。
	DialTimeout time.Duration
	// RequestTimeout 整体请求超时。
	RequestTimeout time.Duration
}

// DefaultImageConfig 是图像 reference URL 下载的默认安全参数。
func DefaultImageConfig() Config {
	return Config{
		MaxBytes:          20 * 1024 * 1024, // 20MB
		MaxRedirects:      3,
		AllowedSchemes:    []string{"https"},
		AllowedImageMimes: []string{"image/png", "image/jpeg", "image/webp"},
		DialTimeout:       10 * time.Second,
		RequestTimeout:    30 * time.Second,
	}
}

// SafeClient 是配置过的 HTTP client，可重复使用。
type SafeClient struct {
	cfg    Config
	client *http.Client
}

// NewSafeClient 按 Config 构造一个 *http.Client，关键路径全部包装：
//   - DialContext 解析 IP 后调用 isPrivateOrLoopback 校验
//   - CheckRedirect 限制跳数 + 重新做 scheme 校验
func NewSafeClient(cfg Config) *SafeClient {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.MaxRedirects == 0 {
		cfg.MaxRedirects = 3
	}
	dialer := &net.Dialer{
		Timeout: cfg.DialTimeout,
		// Control 在 socket connect 之前最后一刻做 IP 黑名单校验——
		// 此时 net.Dial 已经完成 DNS lookup，address 是 "ip:port" 形式，
		// 能防住"DNS 解析时白名单、连接前 rebinding 到内网 IP"的攻击。
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return ErrBlockedIP
			}
			if isBlockedIP(ip) {
				return ErrBlockedIP
			}
			return nil
		},
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.RequestTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	c := &http.Client{
		Timeout:   cfg.RequestTimeout,
		Transport: ipprivacy.NewTransport(transport),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > cfg.MaxRedirects {
				return ErrTooManyRedirects
			}
			if !schemeAllowed(req.URL.Scheme, cfg.AllowedSchemes) {
				return ErrSchemeNotAllowed
			}
			return nil
		},
	}
	return &SafeClient{cfg: cfg, client: c}
}

// FetchImage 安全地下载一张图片，做 scheme/IP/size/MIME 校验。
// 返回字节流 + 解析后的 mime（来自 http.DetectContentType，权威）。
func (s *SafeClient) FetchImage(ctx context.Context, url string) ([]byte, string, error) {
	if err := s.validateScheme(url); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("safehttp: upstream status %d", resp.StatusCode)
	}
	if resp.ContentLength > s.cfg.MaxBytes {
		return nil, "", ErrSizeExceeded
	}
	// LimitReader 设到 MaxBytes+1：读完后若仍有数据可判定超限
	limited := io.LimitReader(resp.Body, s.cfg.MaxBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", err
	}
	if int64(len(buf)) > s.cfg.MaxBytes {
		return nil, "", ErrSizeExceeded
	}
	mime, err := s.ValidateImageBytes(buf, resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", err
	}
	return buf, mime, nil
}

// ValidateImageBytes 对一段已读入内存的字节流做 MIME 双重校验：
//   - 用 http.DetectContentType 嗅探真实类型（基于 magic bytes）
//   - 如果 declaredCT 非空，对比是否一致（防 Content-Type 伪造）
//   - 检查是否在 AllowedImageMimes 白名单内
//
// 返回权威 mime。declaredCT 留空时仅做白名单校验。
//
// SVG 不在白名单中——它是 XML 文本，可执行 JavaScript / 触发 XXE。
func (s *SafeClient) ValidateImageBytes(buf []byte, declaredCT string) (string, error) {
	if len(s.cfg.AllowedImageMimes) == 0 {
		return "", nil
	}
	sniffed := http.DetectContentType(buf)
	// 去掉 "; charset=..." 后缀方便比对
	sniffed = strings.SplitN(sniffed, ";", 2)[0]
	sniffed = strings.TrimSpace(sniffed)

	if !mimeAllowed(sniffed, s.cfg.AllowedImageMimes) {
		return sniffed, fmt.Errorf("%w: sniffed=%s", ErrUnsupportedMime, sniffed)
	}
	if declaredCT != "" {
		declared := strings.SplitN(declaredCT, ";", 2)[0]
		declared = strings.TrimSpace(declared)
		if declared != "" && declared != sniffed {
			return sniffed, fmt.Errorf("%w: declared=%s sniffed=%s",
				ErrMimeDeclaredMismatch, declared, sniffed)
		}
	}
	return sniffed, nil
}

func (s *SafeClient) validateScheme(rawURL string) error {
	for _, sc := range s.cfg.AllowedSchemes {
		if strings.HasPrefix(rawURL, sc+"://") {
			return nil
		}
	}
	return ErrSchemeNotAllowed
}

func schemeAllowed(s string, allowed []string) bool {
	for _, a := range allowed {
		if a == s {
			return true
		}
	}
	return false
}

func mimeAllowed(s string, allowed []string) bool {
	for _, a := range allowed {
		if a == s {
			return true
		}
	}
	return false
}

// isBlockedIP 判断 IP 是否落在 SSRF 风险段：
//   - loopback (127.0.0.0/8, ::1)
//   - link-local (169.254.0.0/16, fe80::/10)
//   - private (RFC1918: 10/8, 172.16/12, 192.168/16)
//   - IPv6 ULA (fc00::/7)
//   - multicast / unspecified
//   - cloud metadata (169.254.169.254 已包含在 link-local，AWS / GCP / Azure 都用这个)
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	// Go 的 IsPrivate 已涵盖 fc00::/7。再加保险：100.64.0.0/10 (CGN)
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && (ip4[1]&0xc0) == 64 {
			return true
		}
	}
	return false
}
