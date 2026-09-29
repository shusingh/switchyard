# Writes event batch payloads encoded by vLLM's own event classes and msgspec
# encoder, as the ZMQ publisher sends them. Run inside the vLLM environment
# from the repository root:
#   python internal/kvevents/testdata/gen_event_batches.py
import msgspec
from vllm.distributed.kv_events import AllBlocksCleared, BlockRemoved, BlockStored, KVEventBatch

h1, h2 = bytes(range(32)), bytes(range(32, 64))
batches = {
    "stored_removed_cleared.bin": KVEventBatch(ts=1.5, events=[
        BlockStored(block_hashes=[h1, h2], parent_block_hash=None, token_ids=list(range(32)),
                    block_size=16, lora_id=None, medium="GPU", lora_name=None),
        BlockRemoved(block_hashes=[h1], medium="GPU"),
        AllBlocksCleared(),
    ]),
    "int_hashes.bin": KVEventBatch(ts=2.0, events=[
        BlockStored(block_hashes=[7, 2**63 + 5], parent_block_hash=3, token_ids=[1, 2],
                    block_size=16, lora_id=None, medium=None, lora_name=None),
    ], data_parallel_rank=0),
}
enc = msgspec.msgpack.Encoder()
for name, batch in batches.items():
    with open(f"internal/kvevents/testdata/{name}", "wb") as f:
        f.write(enc.encode(batch))
    print(name, "written")
