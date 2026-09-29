# Generates the golden vectors in vllm_block_hashes.json with vLLM's own hashing
# functions. Run inside the vLLM environment from the repository root:
#   python internal/prefix/testdata/gen_vllm_block_hashes.py > internal/prefix/testdata/vllm_block_hashes.json
import json, random
from vllm.utils.hashing import sha256_cbor
from vllm.v1.core import kv_cache_utils as k

k.init_none_hash(sha256_cbor)
rng = random.Random(7)
cases = []
for block_size, n in [(16, 16), (16, 40), (16, 160), (4, 9), (16, 33)]:
    # Mix small and large token IDs to exercise every CBOR integer width.
    tokens = [rng.choice([rng.randrange(24), rng.randrange(256), rng.randrange(65536), rng.randrange(151_000), rng.randrange(2**32, 2**33)]) for _ in range(n)]
    parent, hashes = None, []
    for i in range(0, n - n % block_size, block_size):
        parent = k.hash_block_tokens(sha256_cbor, parent, tokens[i:i + block_size], None)
        hashes.append(bytes(parent).hex())
    cases.append({"tokens": tokens, "block_size": block_size, "hashes": hashes})
print(json.dumps({"none_hash": bytes(k.NONE_HASH).hex(), "cases": cases}))
