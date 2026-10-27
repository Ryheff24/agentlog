# Ideas for later

Not planned work. Each entry records what was asked for and why, so it can be
picked up without re-deriving it.

## A standing "what have I been up to" overview

"What have I been working on, and where did I leave off?" is one of the most
common questions asked through agentlog. `agentlog --projects` answers it
once, per repository, but uses a fixed window. The window should follow
recency instead: one week can hold ten threads of work and the next only
three, so the overview should show enough recent work, however far back that
goes. Before `--projects` existed, answering it took about twenty previews by
hand.

## Semantic search, for the questions grep can't answer

Plenty of lookups are better done lexically, and agentlog does those well.
But some questions, like "where did I decide what this word in the spec
means?", are better put to an embedding model than to a chain of greps. The
idea: flatten each event to plaintext (the generic reader already does this),
embed it per event (prompts, replies, compact tool lines, never raw tool
output), and put a lexical index over the same text first, with embeddings
behind the same search API. At least two chunk sizes, a fine one and a coarse
one, and a scaling study before committing. A small model (384-d class) fits
short log events better than a large one. Embedding should run on-device, in
a helper agentlog spawns and talks to over stdio, so agentlog stays pure Go
with no cgo and no external service.

## Handing a session from one harness to another

The original end goal. When one agent runs out of limits mid-task, another
(possibly on a different harness) picks up where it stopped. Summarizing a
whole 400k-token thread in one shot isn't the plan. Instead, a local model
works agentically on the log: given agentlog and a sandbox, it pulls the
specifics the next agent needs, and maps the work into the target harness's
terms. Shrinking tool-call boilerplate when passing context between harnesses
is in scope; dropping tool calls entirely is not.

## A human interface

agentlog is unusable for a person by design. A TUI over the same engine is
wanted, so someone can, for example, open one of fifty subagents without
copying ids around. It belongs to the owner to design; don't plan it.
