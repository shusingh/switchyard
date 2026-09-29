package prefix

import (
	"crypto/sha256"
	"encoding/binary"
)

// VLLMBlockHash is a block hash exactly as vLLM computes it with
// --prefix-caching-hash-algo sha256_cbor.
type VLLMBlockHash [sha256.Size]byte

// vllmNoneHashSeed is the value vLLM hashes to get the parent of a prompt's
// first block when the hash function is cryptographic and PYTHONHASHSEED is
// unset (vllm/v1/core/kv_cache_utils.py, DEFAULT_NONE_HASH_SEED).
const vllmNoneHashSeed = "vllm-none-hash"

// VLLMNoneHash returns the parent hash vLLM uses for a prompt's first block.
func VLLMNoneHash() VLLMBlockHash {
	return sha256.Sum256(appendCBORText(nil, vllmNoneHashSeed))
}

// VLLMBlockHashes reproduces vLLM's prefix-cache block hashes for a prompt's
// token IDs: one hash per full block of blockSize tokens, each covering its
// parent's hash, so it identifies the whole prefix up to that block.
//
// vLLM hashes the tuple (parent hash, block token IDs, extra keys) encoded as
// canonical CBOR with SHA-256. Extra keys (LoRA, multimodal inputs, cache
// salt) are not supported here; for plain text prompts they are None. See
// docs/engineering/design.md section 7.4.
func VLLMBlockHashes(tokens []uint64, blockSize int) []VLLMBlockHash {
	n := len(tokens) / blockSize
	hashes := make([]VLLMBlockHash, n)
	parent := VLLMNoneHash()
	var buf []byte
	for i := range n {
		buf = buf[:0]
		buf = appendCBORHead(buf, cborArray, 3)
		buf = appendCBORHead(buf, cborBytes, uint64(len(parent)))
		buf = append(buf, parent[:]...)
		block := tokens[i*blockSize : (i+1)*blockSize]
		buf = appendCBORHead(buf, cborArray, uint64(len(block)))
		for _, t := range block {
			buf = appendCBORHead(buf, cborUint, t)
		}
		buf = append(buf, cborNull)
		parent = sha256.Sum256(buf)
		hashes[i] = parent
	}
	return hashes
}

// Low64 returns the hash's low 64 bits as vLLM reports them in KV events
// when VLLM_KV_EVENTS_USE_INT_BLOCK_HASHES is set: the big-endian integer
// value of the hash, truncated to 64 bits.
func (h VLLMBlockHash) Low64() uint64 { return binary.BigEndian.Uint64(h[len(h)-8:]) }

// The subset of CBOR (RFC 8949) that vLLM's block keys use, in canonical
// (shortest) form.
const (
	cborUint  = 0
	cborBytes = 2
	cborText  = 3
	cborArray = 4
	cborNull  = 0xf6
)

func appendCBORHead(b []byte, major byte, n uint64) []byte {
	m := major << 5
	switch {
	case n < 24:
		return append(b, m|byte(n))
	case n <= 0xff:
		return append(b, m|24, byte(n))
	case n <= 0xffff:
		return binary.BigEndian.AppendUint16(append(b, m|25), uint16(n))
	case n <= 0xffffffff:
		return binary.BigEndian.AppendUint32(append(b, m|26), uint32(n))
	default:
		return binary.BigEndian.AppendUint64(append(b, m|27), n)
	}
}

func appendCBORText(b []byte, s string) []byte {
	return append(appendCBORHead(b, cborText, uint64(len(s))), s...)
}
