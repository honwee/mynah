// Provider factory: config.llm -> a core.ChatProvider. The registry is
// compile-time (Go plugins are too brittle); adding a provider = one adapter
// file + one case here + a console form variant.
package llm

import (
	"fmt"

	"mynah/core"
)

// Providers lists the selectable provider keys, in console display order.
var Providers = []string{"openai", "dify", "coze"}

// FromConfig builds the provider for the given llm config values. An empty
// provider means "openai" (pre-provider configs and flag-only setups).
// stream off = wait for the complete reply before TTS; coze ignores it (the
// platform always streams, and its non-streaming API needs a polling dance
// that isn't worth the surface).
func FromConfig(provider, baseURL, model, apiKey, system, botID string, stream bool) (core.ChatProvider, error) {
	switch provider {
	case "", "openai":
		c := New(baseURL, model, apiKey, system)
		c.Streaming = stream
		return c, nil
	case "dify":
		if apiKey == "" {
			return nil, fmt.Errorf("dify provider requires api_key")
		}
		d := NewDify(baseURL, apiKey)
		d.Streaming = stream
		return d, nil
	case "coze":
		if apiKey == "" || botID == "" {
			return nil, fmt.Errorf("coze provider requires api_key and bot_id")
		}
		return NewCoze(baseURL, apiKey, botID), nil
	}
	return nil, fmt.Errorf("unknown llm provider %q (want openai/dify/coze)", provider)
}
