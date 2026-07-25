package gemini

// Gemini API request/response types

type request struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	Tools             []geminiTool      `json:"tools,omitempty"`
	ToolConfig        *toolConfig       `json:"toolConfig,omitempty"`
	// SafetySettings is forwarded as-is from the unified request. Default
	// thresholds block more aggressively than many users want; providing this
	// hook lets clients tune per-category thresholds.
	SafetySettings any `json:"safetySettings,omitempty"`
	// CachedContent references a pre-created cached content resource (full
	// name like "cachedContents/abc123"). When set, Gemini reuses the cached
	// prefix at reduced billing.
	CachedContent string `json:"cachedContent,omitempty"`
	// Labels are user-supplied tags (string→string map) attached to the
	// request for billing / monitoring grouping. Forwarded as-is.
	Labels map[string]string `json:"labels,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string            `json:"text,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
	InlineData       *inlineData       `json:"inlineData,omitempty"`
	Thought          *bool             `json:"thought,omitempty"` // Gemini thinking indicator
	// ThoughtSignature carries Gemini's encrypted reasoning state. Returned on
	// assistant parts when thinking is active; Gemini 3 strictly requires it
	// echoed back on the next turn during multi-turn function calling — a
	// missing signature returns 400. The gateway captures the first non-empty
	// signature in the response and re-emits it on the model's first part on
	// the next request.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

type functionCall struct {
	Name string `json:"name"`
	Args any    `json:"args,omitempty"`
}

type functionResponse struct {
	Name     string `json:"name"`
	Response any    `json:"response"`
}

type inlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiTool struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations,omitempty"`
	// GoogleSearch enables Gemini's built-in web grounding. Empty object turns
	// it on; nil omits the field entirely. Only Gemini 2.0+ / 2.5 / 3.x models
	// honor this — earlier versions used the now-deprecated googleSearchRetrieval.
	GoogleSearch *googleSearchTool `json:"googleSearch,omitempty"`
	// URLContext enables Gemini's url_context tool (Gemini 3+) which lets the
	// model fetch and read the body of URLs the user provides. Empty object
	// turns it on; nil omits.
	URLContext *urlContextTool `json:"urlContext,omitempty"`
	// CodeExecution enables Gemini's built-in Python code execution sandbox
	// (Gemini 3+). The model emits `executable_code` and
	// `code_execution_result` parts in the response which the gateway
	// surfaces via ProviderTurnBlocks for round-trip. Empty object turns it
	// on; nil omits.
	CodeExecution *codeExecutionTool `json:"codeExecution,omitempty"`
}

// googleSearchTool is currently a marker (no fields), but kept as a struct so
// the encoder emits {} rather than null when the tool is enabled.
type googleSearchTool struct{}

// urlContextTool mirrors googleSearchTool — empty marker that serializes as {}.
type urlContextTool struct{}

// codeExecutionTool mirrors googleSearchTool — empty marker that serializes as {}.
type codeExecutionTool struct{}

type functionDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type toolConfig struct {
	FunctionCallingConfig *functionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type functionCallingConfig struct {
	Mode                 string   `json:"mode"`                           // "AUTO", "ANY", "NONE"
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"` // restrict to specific functions
}

type generationConfig struct {
	MaxOutputTokens  *int     `json:"maxOutputTokens,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"topP,omitempty"`
	TopK             *int     `json:"topK,omitempty"`
	StopSequences    []string `json:"stopSequences,omitempty"`
	ResponseMimeType string   `json:"responseMimeType,omitempty"`
	// ResponseSchema accepts an OpenAPI 3 Schema subset — no $ref, allOf,
	// oneOf, etc. Used as a fallback when the client provided no schema or a
	// trivial one.
	ResponseSchema any `json:"responseSchema,omitempty"`
	// ResponseJSONSchema accepts the full JSON Schema spec (incl. $ref,
	// $defs, allOf, oneOf, anyOf, additionalProperties) on Gemini 2.5+.
	// Preferred path when the client supplied a json_schema response format
	// — OpenAI's strict json_schema typically hits OpenAPI-subset limits.
	// Mutually exclusive with ResponseSchema.
	ResponseJSONSchema any                   `json:"responseJsonSchema,omitempty"`
	FrequencyPenalty   *float64              `json:"frequencyPenalty,omitempty"`
	PresencePenalty    *float64              `json:"presencePenalty,omitempty"`
	Seed               *int                  `json:"seed,omitempty"`
	CandidateCount     *int                  `json:"candidateCount,omitempty"`
	ResponseLogprobs   *bool                 `json:"responseLogprobs,omitempty"`
	Logprobs           *int                  `json:"logprobs,omitempty"`
	ThinkingConfig     *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type geminiThinkingConfig struct {
	// IncludeThoughts must be true for the model to actually return its thought
	// summary parts. Setting only thinkingBudget makes the model think but the
	// thoughts never reach the client. See:
	// https://ai.google.dev/gemini-api/docs/thinking
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
	ThinkingBudget  int  `json:"thinkingBudget,omitempty"`
}

type response struct {
	Candidates    []candidate    `json:"candidates"`
	UsageMetadata *usageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string         `json:"modelVersion,omitempty"`
	// PromptFeedback is set when Gemini blocks the prompt outright (e.g. due
	// to safety policies); in that case Candidates is empty.
	PromptFeedback *promptFeedback `json:"promptFeedback,omitempty"`
}

type promptFeedback struct {
	BlockReason string `json:"blockReason,omitempty"`
}

type candidate struct {
	Content      *content `json:"content,omitempty"`
	FinishReason string   `json:"finishReason,omitempty"`
	// Index distinguishes candidates when CandidateCount > 1. Gemini sends
	// the same index across chunks for the same candidate during streaming.
	Index int `json:"index,omitempty"`
	// GroundingMetadata is set when google_search grounding produced web
	// citations. The gateway flattens this into OpenAI-style
	// message.annotations url_citation entries.
	GroundingMetadata *groundingMetadata `json:"groundingMetadata,omitempty"`
}

type groundingMetadata struct {
	WebSearchQueries  []string           `json:"webSearchQueries,omitempty"`
	GroundingChunks   []groundingChunk   `json:"groundingChunks,omitempty"`
	GroundingSupports []groundingSupport `json:"groundingSupports,omitempty"`
}

type groundingChunk struct {
	Web *groundingChunkWeb `json:"web,omitempty"`
}

type groundingChunkWeb struct {
	URI   string `json:"uri,omitempty"`
	Title string `json:"title,omitempty"`
}

type groundingSupport struct {
	Segment               *groundingSegment `json:"segment,omitempty"`
	GroundingChunkIndices []int             `json:"groundingChunkIndices,omitempty"`
}

type groundingSegment struct {
	StartIndex int    `json:"startIndex,omitempty"`
	EndIndex   int    `json:"endIndex,omitempty"`
	Text       string `json:"text,omitempty"`
}

type usageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	// ThoughtsTokenCount is billed alongside output for Gemini 2.5/3 thinking
	// models even when only a summary is returned. Maps to OpenAI-compat
	// completion_tokens_details.reasoning_tokens.
	ThoughtsTokenCount int `json:"thoughtsTokenCount"`
}
