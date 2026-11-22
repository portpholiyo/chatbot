# 0002. Two-stage LLM pipeline: cheap guard, then answer

- Status: Accepted
- Date: 2026-11-22

## Context
The bot must answer only questions about one person. The answer call carries the whole knowledge text (thousands of input tokens), so spending it on off-topic or abusive prompts is wasteful and a larger attack surface.

## Options considered
1. One call with a restrictive system prompt: simple, but every abusive prompt pays the full cost and the model alone decides refusals.
2. Keyword/regex filtering: brittle and easy to bypass.
3. A small, cheap classifier call (`ALLOW`/`BLOCK`) before the answer call.

## Decision
Option 3. The guard sees the recent turns and the latest message (not the knowledge text) and is capped at a handful of output tokens. Any guard error counts as `BLOCK` (fail closed).

## Consequences
- Off-topic traffic costs a fraction of an answer.
- Added latency of one small call on the happy path.
- Guard and answer prompts evolve separately and are tested separately.
- Open item: prompt caching for the knowledge text.
