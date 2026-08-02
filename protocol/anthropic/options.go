package anthropic

// DefaultVersion is Anthropic's stable API version used when a caller does not
// explicitly select another version.
const DefaultVersion = "2023-06-01"

// RequestOptions contains request-scoped Anthropic protocol headers. These
// values are transport metadata and are never encoded into a JSON body.
type RequestOptions struct {
	Version string   `json:"-"`
	Betas   []string `json:"-"`
}

// RequestOption mutates request-scoped protocol options.
type RequestOption func(*RequestOptions)

// ApplyRequestOptions applies options to the documented default configuration.
// Returned slices do not alias slices owned by the caller.
func ApplyRequestOptions(options ...RequestOption) RequestOptions {
	result := RequestOptions{Version: DefaultVersion}
	for _, option := range options {
		if option != nil {
			option(&result)
		}
	}
	result.Betas = append([]string(nil), result.Betas...)
	return result
}

// WithVersion overrides the anthropic-version header for one operation.
func WithVersion(version string) RequestOption {
	return func(options *RequestOptions) {
		options.Version = version
	}
}

// WithBetas replaces the anthropic-beta values for one operation. Values are
// intentionally strings rather than a closed enum because Anthropic adds beta
// identifiers independently of SDK releases.
func WithBetas(betas ...string) RequestOption {
	copied := append([]string(nil), betas...)
	return func(options *RequestOptions) {
		options.Betas = append(options.Betas[:0], copied...)
	}
}
