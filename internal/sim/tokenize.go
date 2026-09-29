package sim

import (
	"encoding/binary"
	"encoding/json"
	"hash/maphash"
	"math"
)

// The simulated tokenizer treats every CostModel.BytesPerToken bytes of
// rendered prompt text as one token. It is deliberately independent of how the
// router keys prefixes: the router hashes the raw request in its own block
// size, while the engine hashes a rendered chat template in 16-token blocks,
// so the router's predictions meet the same kind of boundary mismatch they
// would meet against a real engine.

// prompt is the rendered, tokenized form of a request.
type prompt struct {
	tokens int
	// blocks holds the chained hash of every full block of blockTokens
	// tokens, in order. A partial final block is not hashed, because the
	// engine only caches full blocks.
	blocks []uint64
}

// chatRequest holds the parts of a request that shape its prompt.
type chatRequest struct {
	Model    string          `json:"model"`
	Messages []chatMessage   `json:"messages"`
	Tools    json.RawMessage `json:"tools"`
	Prompt   json.RawMessage `json:"prompt"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// render produces the text the model would see, in the spirit of a chat
// template: tools first, then each message wrapped in role markers.
func render(req *chatRequest, chat bool) []byte {
	if !chat {
		return req.Prompt
	}
	var b []byte
	if len(req.Tools) > 0 {
		b = append(b, "<|tools|>\n"...)
		b = append(b, req.Tools...)
		b = append(b, '\n')
	}
	for _, m := range req.Messages {
		b = append(b, "<|"...)
		b = append(b, m.Role...)
		b = append(b, "|>\n"...)
		b = append(b, m.Content...)
		b = append(b, "\n<|end|>\n"...)
	}
	return append(b, "<|assistant|>\n"...)
}

// tokenizer turns rendered text into a prompt with chained block hashes.
type tokenizer struct {
	seed          maphash.Seed
	blockTokens   int
	bytesPerToken float64
}

func newTokenizer(blockTokens int, bytesPerToken float64) *tokenizer {
	return &tokenizer{seed: maphash.MakeSeed(), blockTokens: blockTokens, bytesPerToken: bytesPerToken}
}

func (t *tokenizer) tokenize(text []byte) prompt {
	p := prompt{tokens: int(math.Ceil(float64(len(text)) / t.bytesPerToken))}
	blockBytes := int(math.Round(float64(t.blockTokens) * t.bytesPerToken))
	var parent [8]byte
	for off := 0; off+blockBytes <= len(text); off += blockBytes {
		var h maphash.Hash
		h.SetSeed(t.seed)
		_, _ = h.Write(parent[:])
		_, _ = h.Write(text[off : off+blockBytes])
		sum := h.Sum64()
		p.blocks = append(p.blocks, sum)
		binary.LittleEndian.PutUint64(parent[:], sum)
	}
	return p
}
