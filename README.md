# Switchyard

A prefix-cache-aware router for LLM inference. Switchyard sits in front of a
pool of OpenAI-compatible model servers (vLLM) and sends each request to the
replica that can serve it fastest, weighing how much of the prompt that replica
already has cached against how busy it is.

**Status:** in development. See [the design](_planning/DESIGN.md) and
[the plan](_planning/PLAN.md).
