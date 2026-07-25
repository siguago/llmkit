package siliconflow

import "github.com/siguago/llmkit/provider/compat"

const defaultBaseURL = "https://api.siliconflow.cn/v1"

// New constructs a SiliconFlow provider. Pass an empty baseURL to use the
// default mainland China endpoint.
func New(baseURL string) *compat.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return compat.New(compat.Config{
		ProviderName: "siliconflow",
		BaseURL:      baseURL,
	})
}
