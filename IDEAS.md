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

agentlog is built for agents, and a person will find it too dense. A view for
people is still useful, for example to open one of fifty subagents or a
headless session without copying ids around. Two things should hold for it:

- **agentlog stays self-contained.** It remains one read-only binary for
  agents. A human interface doesn't add a TUI, a server or state to it.
- **The interface is separate.** It's its own program over the same logs. The
  owner already uses one every day, a dashboard that runs on localhost and
  isn't published yet. Anyone is welcome to build their own.

## Feedback that becomes issues

The feedback loop exists for agentlog's own development: finding where it
fails, and debugging it. It isn't doing that. Too many notes say the tool
worked, which helps nobody, and the notes that report a problem rarely carry
enough to reproduce it. Problems happen in very specific settings, and the
setting is what's missing.

A note should be one of two things: a problem, or something that would make
the tool better. Otherwise, nothing. The instruction agents get, and the help
for `feedback add`, should say so.

The setting is already on the machine: the agentlog calls the agent made
before writing the note (in the same session's log), their durations and
sizes (the run log), and the harness versions (`doctor`). Gathering those
with the note would make a report someone else can act on, from a machine
and logs they'll never see. That matters more now that other people use it.
How that report reaches the repository, and what it must leave out, is still
open.

Why this matters more than a redesign: what has matured is the agentic use,
not the code. Billions of tokens have gone through agentlog on one machine,
and agents using it stochastically, with real objectives, is in effect the
test suite. Golden prompts and simulations don't capture that, which is why
large changes to the surface are held back. Real failure reports, with their
setting, are the closest thing to a reproducible test.
