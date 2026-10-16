package antigravity

import "github.com/jzPPP3333/allproxyapi/internal/pool"

// Catalog is the single source of truth for the models this provider serves.
//
// Every id here is a real upstream model name that NormalizeModel returns
// unchanged. That invariant is what keeps the advertised list honest: a client
// that picks a name out of /v1/models gets exactly that model, rather than a
// friendly alias that the adapter silently rewrites into something else.
//
// Human-facing aliases (claude-3-7-sonnet and friends) are still accepted on
// input through NormalizeModel and the routing rules, but they are not
// advertised as distinct model ids, because two ids that resolve to one
// upstream model are two names for the same thing.
var Catalog = []pool.ModelMetadata{
	{
		ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash",
		DescriptionZh:     "Google 极速轻量模型，低延迟高吞吐",
		SupportsToolCall:  true,
		SupportsImages:    true,
		SupportsReasoning: true,
	},
	{
		ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro",
		DescriptionZh:     "Google 旗舰深度推理模型，复杂编码首选",
		SupportsToolCall:  true,
		SupportsImages:    true,
		SupportsReasoning: true,
	},
	{
		ID: "gemini-2.5-flash-thinking", Name: "Gemini 2.5 Flash Thinking",
		DescriptionZh:     "具备显式思维链的 Gemini 增强模型",
		SupportsToolCall:  true,
		SupportsReasoning: true,
	},
	{
		ID: "gemini-3-flash", Name: "Gemini 3 Flash",
		DescriptionZh:    "新一代 Gemini 3 早期体验版本",
		SupportsToolCall: true,
		SupportsImages:   true,
	},
	{
		ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6",
		DescriptionZh:     "Anthropic 混合推理模型，编码与长上下文均衡",
		SupportsToolCall:  true,
		SupportsImages:    true,
		SupportsReasoning: true,
	},
	{
		ID: "claude-opus-4-6-thinking", Name: "Claude Opus 4.6 Thinking",
		DescriptionZh:     "Anthropic 顶级能力思考增强模型",
		SupportsToolCall:  true,
		SupportsReasoning: true,
	},
}

// CatalogIDs is the string form of Catalog, used by the Upstream interface.
func CatalogIDs() []string {
	ids := make([]string, 0, len(Catalog))
	for _, m := range Catalog {
		ids = append(ids, m.ID)
	}
	return ids
}

// CatalogCopy returns a copy of the catalog so a caller cannot mutate the
// package-level slice through an account's cached model list.
func CatalogCopy() []pool.ModelMetadata {
	out := make([]pool.ModelMetadata, len(Catalog))
	copy(out, Catalog)
	return out
}
