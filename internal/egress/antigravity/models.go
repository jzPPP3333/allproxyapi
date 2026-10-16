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
		ID: "gemini-3-flash", Name: "Gemini 3 Flash",
		DescriptionZh:     "Google 新一代极速模型",
		SupportsToolCall:  true,
		SupportsImages:    true,
		SupportsReasoning: true,
	},
	{
		ID: "gemini-3.8-flash-high", Name: "Gemini 3.8 Flash",
		DescriptionZh:     "Gemini 3.8 高算力档",
		SupportsToolCall:  true,
		SupportsImages:    true,
		SupportsReasoning: true,
	},
	{
		ID: "gemini-3.1-flash-lite", Name: "Gemini 3.1 Flash Lite",
		DescriptionZh:     "Gemini 3.1 轻量档，低延迟",
		SupportsToolCall:  true,
		SupportsReasoning: true,
	},
	{
		ID: "gemini-3.5-flash-lite", Name: "Gemini 3.5 Flash Lite",
		DescriptionZh:     "Gemini 3.5 轻量档",
		SupportsToolCall:  true,
		SupportsReasoning: true,
	},
	{
		ID: "gemini-3.1-flash-image", Name: "Gemini 3.1 Flash Image",
		DescriptionZh:    "Gemini 3.1 图像理解档",
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
