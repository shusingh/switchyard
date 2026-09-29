package sim

import (
	"strconv"
	"strings"
	"time"

	"github.com/shusingh/switchyard/internal/openai"
)

// generation builds the response payloads for one simulated request. Payload
// shapes follow what vLLM emits, field for field where the router cares.
type generation struct {
	id     string
	model  string
	chat   bool
	tokens int
	// promptTokens is reported in usage.
	promptTokens int
}

func (g generation) object(streaming bool) string {
	switch {
	case g.chat && streaming:
		return "chat.completion.chunk"
	case g.chat:
		return "chat.completion"
	default:
		return "text_completion"
	}
}

func (g generation) base(streaming bool) map[string]any {
	return map[string]any{
		"id":      g.id,
		"object":  g.object(streaming),
		"created": time.Now().Unix(),
		"model":   g.model,
	}
}

func token(i int) string { return "tok" + strconv.Itoa(i) + " " }

func (g generation) text() string {
	var b strings.Builder
	for i := range g.tokens {
		b.WriteString(token(i))
	}
	return b.String()
}

func (g generation) usage() openai.Usage {
	return openai.Usage{PromptTokens: g.promptTokens, CompletionTokens: g.tokens, TotalTokens: g.promptTokens + g.tokens}
}

func (g generation) choice(content string, finish any) map[string]any {
	c := map[string]any{"index": 0, "finish_reason": finish}
	if g.chat {
		c["delta"] = map[string]any{"content": content}
	} else {
		c["text"] = content
	}
	return c
}

// roleChunk mirrors vLLM's first chat chunk: the assistant role with an empty
// content delta. It is not a token.
func (g generation) roleChunk() map[string]any {
	m := g.base(true)
	m["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil}}
	return m
}

func (g generation) tokenChunk(i int) map[string]any {
	m := g.base(true)
	m["choices"] = []any{g.choice(token(i), nil)}
	return m
}

func (g generation) finishChunk() map[string]any {
	m := g.base(true)
	m["choices"] = []any{g.choice("", "stop")}
	return m
}

func (g generation) usageChunk() map[string]any {
	m := g.base(true)
	m["choices"] = []any{}
	m["usage"] = g.usage()
	return m
}

func (g generation) response() map[string]any {
	m := g.base(false)
	choice := map[string]any{"index": 0, "finish_reason": "stop"}
	if g.chat {
		choice["message"] = map[string]any{"role": "assistant", "content": g.text()}
	} else {
		choice["text"] = g.text()
	}
	m["choices"] = []any{choice}
	m["usage"] = g.usage()
	return m
}
