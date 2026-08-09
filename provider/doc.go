// Package provider defines the wire types and adapter contracts that sit under
// the llmkit façade.
//
// Most users never import this package: llmkit.Client wraps everything here in
// a smaller surface. Reach for it when you are writing your own adapter, or when
// you need a vendor-specific field the façade does not re-export.
//
// # Adapter contracts
//
// [Provider] is the one interface every adapter must satisfy — chat, streaming
// chat, and a name. Everything else is optional and discovered by type
// assertion: [ModelLister], [ModelTaskLister], [Embedder], [Reranker],
// [ImageGenerator], [ImageEditor], [VideoCreator], [VideoCanceller],
// [ResponsesCreator], [ResponsesStreamer],
// [ResponsesRetriever], [ResponsesCanceller], [ResponsesDeleter],
// [ResponsesInputItemLister], [ResponsesTokenCounter],
// [AnthropicMessagesCreator], [AnthropicMessagesStreamer], and
// [AnthropicTokenCounter].
//
// Implement an optional interface only when the vendor endpoint genuinely
// exists. Do not satisfy one with a method that always returns [ErrUnsupported]:
// the assertion would succeed and llmkit.Client's capability probes would report
// a capability the caller cannot actually use. An adapter that generates images
// but cannot edit them implements [ImageGenerator] and stops there.
//
// Native protocol capabilities stay separate both from [Provider] and from one
// another. A relay that implements Responses create but not retrieval should
// implement [ResponsesCreator] only. Do not add native methods to a broadly
// embedded compatibility provider: Go method promotion would make every adapter
// embedding it falsely advertise that endpoint.
//
// Native wire data belongs to leaf protocol packages rather than this package:
// OpenAI Responses types are in [github.com/siguago/llmkit/protocol/responses]
// and Anthropic Messages types are in
// [github.com/siguago/llmkit/protocol/anthropic]. The llmkit façade exposes
// explicit methods for them; it does not automatically route by model name or
// promise a lossless conversion between the two protocols.
//
// # API stability
//
// This module is pre-1.0 and the API is not frozen. Within that, the parts here
// differ in how settled they are:
//
//   - Stable in shape. [Provider], the mature unified capability interfaces,
//     [ProviderError], [ChatCompletionRequest]/[ChatCompletionResponse] core
//     fields (Model, Messages, Temperature, MaxTokens, Tools, ToolChoice,
//     ResponseFormat, Stream), [Message], and [ContentPart]. These mirror the
//     established unified wire format and are unlikely to move.
//
//   - Additive billing metadata. The established [Usage] counters retain their
//     meanings, but new independently billed dimensions may be added as named
//     fields. Its exact field count is therefore not stable; use keyed composite
//     literals instead of positional ones.
//
//   - Additive and vendor-specific. The long tail of optional fields on
//     [ChatCompletionRequest] — ProviderRouting, SafetySettings, CacheID,
//     BotSetting, ChatTemplateKwargs, and the rest. Each exists because one
//     vendor needed it; providers that don't recognize a field ignore it.
//     Expect this set to grow, and expect individual fields to be retired when
//     a vendor drops the feature.
//
//   - Native and forward-compatible. The core typed variants in the protocol
//     leaf packages track the corresponding vendor wire contracts. Unknown
//     item, block, event, and extension variants are retained as raw JSON so a
//     newer server response is not silently discarded. A Raw value is an
//     escape hatch, not a claim that every adjacent vendor product is covered.
//
//   - Opaque passthrough. Anything typed `any` or `map[string]any` is forwarded
//     to the vendor without interpretation. The SDK makes no promise about the
//     shape, because the vendor owns it. Consult the vendor's docs, and pin
//     nothing on these staying JSON-compatible across a vendor's own changes.
//
// Fields marked Deprecated in their doc comment will be removed in a future
// pre-1.0 release, not merely discouraged.
package provider
