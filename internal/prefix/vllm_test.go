package prefix

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The golden file was produced by vLLM 0.30.0's own hashing functions
// (hash_block_tokens with sha256_cbor) by testdata/gen_vllm_block_hashes.py.
// Matching it proves the router can compute the exact keys vLLM publishes in
// its KV cache events.
type vllmGolden struct {
	NoneHash string `json:"none_hash"`
	Cases    []struct {
		Tokens    []uint64 `json:"tokens"`
		BlockSize int      `json:"block_size"`
		Hashes    []string `json:"hashes"`
	} `json:"cases"`
}

func TestVLLMBlockHashesMatchVLLM(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/vllm_block_hashes.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden vllmGolden
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	none := VLLMNoneHash()
	if got := hex.EncodeToString(none[:]); got != golden.NoneHash {
		t.Fatalf("none hash = %s, want %s", got, golden.NoneHash)
	}
	for i, c := range golden.Cases {
		got := VLLMBlockHashes(c.Tokens, c.BlockSize)
		if len(got) != len(c.Hashes) {
			t.Fatalf("case %d: %d hashes, want %d", i, len(got), len(c.Hashes))
		}
		for j, h := range got {
			if hex.EncodeToString(h[:]) != c.Hashes[j] {
				t.Errorf("case %d block %d: hash %x, want %s", i, j, h, c.Hashes[j])
			}
		}
	}
}

func TestVLLMBlockHashLow64(t *testing.T) {
	t.Parallel()
	var h VLLMBlockHash
	for i := range h {
		h[i] = byte(i)
	}
	// The low 64 bits of the big-endian integer are the last 8 bytes.
	if got, want := h.Low64(), uint64(0x18191a1b1c1d1e1f); got != want {
		t.Errorf("Low64 = %#x, want %#x", got, want)
	}
}
