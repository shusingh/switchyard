// Package prefix turns requests into prefix keys and tracks which backends
// are believed to hold each prefix in their KV cache.
//
// A key is a chain of block hashes over a canonical byte form of the request:
// block i's hash covers every byte before it, so two requests share their
// first n hashes exactly when their canonical forms share the first n blocks.
// See docs/engineering/design.md sections 6 and 7.
package prefix

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/maphash"

	"github.com/shusingh/switchyard/internal/openai"
)

// Separators between canonical fields. They are ASCII control bytes that
// cannot appear unescaped in JSON, so field boundaries are never ambiguous.
const (
	sepValue  = 0x00
	sepRecord = 0x1e
)

// Keyer computes prefix keys. It is safe for concurrent use.
type Keyer struct {
	seed       maphash.Seed
	blockBytes int
	maxBlocks  int
}

// NewKeyer returns a Keyer that hashes blockBytes-byte blocks and keys at most
// maxBlocks blocks of any request.
func NewKeyer(blockBytes, maxBlocks int) *Keyer {
	return &Keyer{seed: maphash.MakeSeed(), blockBytes: blockBytes, maxBlocks: maxBlocks}
}

// BlockBytes returns the size of one hashed block.
func (k *Keyer) BlockBytes() int { return k.blockBytes }

// Request is a decoded request: the fields routing needs and its prefix key.
type Request struct {
	Fields openai.RoutingFields
	// Hashes is the chain of block hashes, one per full block of the
	// canonical form, capped at the Keyer's maximum.
	Hashes []uint64
	// CanonicalBytes is the length of the canonical form, a proxy for prompt
	// size used to estimate tokens.
	CanonicalBytes int
}

// body mirrors the parts of a completions or chat completions request that
// determine the prompt. Content fields stay raw: the key must reflect the
// exact bytes the client sent, and not decoding them saves allocation.
type body struct {
	openai.RoutingFields
	Tools    json.RawMessage `json:"tools"`
	Messages []struct {
		Role       string          `json:"role"`
		Name       string          `json:"name"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  json.RawMessage `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
	} `json:"messages"`
	Prompt json.RawMessage `json:"prompt"`
}

// Parse decodes a request body once, returning its routing fields and prefix
// key. chat selects the chat completions layout; otherwise the body is a
// legacy completions request.
func (k *Keyer) Parse(raw []byte, chat bool) (Request, error) {
	var b body
	if err := json.Unmarshal(raw, &b); err != nil {
		return Request{}, fmt.Errorf("decode request body: %w", err)
	}
	if b.Model == "" {
		return Request{}, openai.ErrMissingModel
	}
	canonical := canonicalize(&b, chat)
	return Request{Fields: b.RoutingFields, Hashes: k.hashBlocks(canonical), CanonicalBytes: len(canonical)}, nil
}

// canonicalize lays the prompt-determining fields out in the order a chat
// template renders them: model, tools, then each message. Byte-prefix sharing
// of this form tracks token-prefix sharing of the rendered prompt.
func canonicalize(b *body, chat bool) []byte {
	size := len(b.Model) + len(b.Tools) + len(b.Prompt) + 16
	for _, m := range b.Messages {
		size += len(m.Role) + len(m.Name) + len(m.Content) + len(m.ToolCalls) + len(m.ToolCallID) + 8
	}
	c := make([]byte, 0, size)
	c = append(c, b.Model...)
	c = append(c, sepRecord)
	if !chat {
		return append(c, b.Prompt...)
	}
	if len(b.Tools) > 0 {
		c = append(c, b.Tools...)
		c = append(c, sepRecord)
	}
	for _, m := range b.Messages {
		c = append(c, m.Role...)
		c = append(c, sepValue)
		c = append(c, m.Name...)
		c = append(c, sepValue)
		c = append(c, m.Content...)
		c = append(c, sepValue)
		c = append(c, m.ToolCalls...)
		c = append(c, sepValue)
		c = append(c, m.ToolCallID...)
		c = append(c, sepRecord)
	}
	return c
}

// hashBlocks returns the chained hash of every full block of c, up to the
// block cap. A trailing partial block is not hashed: engines only cache full
// blocks, so it cannot be a cache hit.
func (k *Keyer) hashBlocks(c []byte) []uint64 {
	n := min(len(c)/k.blockBytes, k.maxBlocks)
	hashes := make([]uint64, n)
	var h maphash.Hash
	h.SetSeed(k.seed)
	var parent [8]byte
	for i := range n {
		h.Reset()
		_, _ = h.Write(parent[:])
		_, _ = h.Write(c[i*k.blockBytes : (i+1)*k.blockBytes])
		hashes[i] = h.Sum64()
		binary.LittleEndian.PutUint64(parent[:], hashes[i])
	}
	return hashes
}
