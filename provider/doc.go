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
// assertion: [ModelLister], [Embedder], [ImageGenerator], [ImageEditor],
// [VideoCreator], [VideoCanceller].
//
// Implement an optional interface only when the vendor endpoint genuinely
// exists. Do not satisfy one with a method that always returns [ErrUnsupported]:
// the assertion would succeed and llmkit.Client's capability probes would report
// a capability the caller cannot actually use. An adapter that generates images
// but cannot edit them implements [ImageGenerator] and stops there.
//
// # API stability
//
// This module is pre-1.0 and the API is not frozen. Within that, the parts here
// differ in how settled they are:
//
//   - Stable in shape. The adapter interfaces above, [ProviderError],
//     [ChatCompletionRequest]/[ChatCompletionResponse] core fields (Model,
//     Messages, Temperature, MaxTokens, Tools, ToolChoice, ResponseFormat,
//     Stream), [Message], [ContentPart], and [Usage] token counts. These
//     mirror the OpenAI wire format and are unlikely to move.
//
//   - Additive and vendor-specific. The long tail of optional fields on
//     [ChatCompletionRequest] — ProviderRouting, SafetySettings, CacheID,
//     BotSetting, ChatTemplateKwargs, and the rest. Each exists because one
//     vendor needed it; providers that don't recognize a field ignore it.
//     Expect this set to grow, and expect individual fields to be retired when
//     a vendor drops the feature.
//
//   - Opaque passthrough. Anything typed `any` or `map[string]any` is forwarded
//     to the vendor without interpretation. The SDK makes no promise about the
//     shape, because the vendor owns it. Consult the vendor's docs, and pin
//     nothing on these staying JSON-compatible across a vendor's own changes.
//
// Fields marked Deprecated in their doc comment will be removed in a future
// pre-1.0 release, not merely discouraged.
package provider
