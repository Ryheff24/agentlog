# CLAUDE.md — agentlog

## Who this is for

Agents are the users of agentlog, and agents are also the ones building it. The
owner barely uses it. Run `agentlog` bare and you'll see it isn't built for a
person, and that's intended. I tried it myself, personally its too dense. 

It exists because agents kept writing one-off heredocs and parsers to find
another agent's session log. That started with an agent that had to find a
Codex thread that ran out of limits mid-fix
(`agentlog <session-id>#1 --full`, `#316`, `#580`). The goal is for agents to find
out what another agent did, consumed or produced while using as little context
and time as possible.

So the person who judges whether a change works is an agent. You are one, so
test it the way one would use it.

## The loop

1. **Reproduce first.** For each reported problem, reproduce it against real logs, then
   decide what to do and write the decision down:
 - A reproduced problem gets an entry under Open in `ISSUES.md`. Delete the
 entry in the commit that fixes it; that commit is the record.
   - A want that's too big to build now goes in `IDEAS.md`, with what was asked
     for.
   - A note you decide not to act on is valid, but record it in `ISSUES.md`
     under "Not acting on", with the reason. A note must never just disappear.

Order: bugs come before features. A feature is worth building when it makes
agents' work cheaper: fewer calls, fewer bytes into context, less waiting, or
no need for a one-off script. `agentlog-usage` measures the first three; see
"Measuring what it costs" in docs/reference.md. Check it before and after a change
that should move those numbers, and say what it showed.

Don't drop an issue because it's hard. Fix it.

## Every change: unit tests, then an end-to-end test

**Unit tests.** Every feature, fix, and any system it adds gets extensive tests
of its behaviour, including the edge cases real logs throw at it.
- Fixtures are shaped like real logs: the machine-wide world in
  `internal/app/world_test.go`, and the redacted record shapes in
  `internal/shapes/testdata`.
- `scripts/coverage.sh --check` must pass before committing; the goal is 95%.
- After adding tests, run `scripts/coverage.sh --update` to record the new
  baseline.
- Test behaviour, not lines. Code that no real case reaches is a candidate for
  deletion.

**End-to-end test.** Passing unit tests doesn't finish a change. Afterwards,
drive the installed binary the way an agent would.
- Pick a real question, aimed at the feature or bug you just touched, and answer
  it on this machine's logs using only `agentlog`. Example: "find the Codex
  session in this directory that was mid-fix when limits ran out, and tell me
  what was left." A new flag gets a question that needs that flag; a bug fix
  gets the question that exposed the bug.
- Check the answer against ground truth, never against agentlog itself: the raw
  JSONL, file mtimes, git history, what the session actually did. An answer
  that's too old, too narrow, misses the second or third session, or misses the
  subagents that did the work is a failure, even if every test passes.
- Note what you asked, what came back, how you checked it, and what it cost
  (calls, bytes, seconds).
- If the answer was wrong, that's a new issue: reproduce it, add it to
  `ISSUES.md`, fix it, and run the end-to-end test again.
- Say in your report to the owner what the end-to-end test was and how it came
  out.

## Things that stay true

- **Log formats drift.** The harnesses change their formats about weekly, and
  agentlog must degrade, never break or go silent. See "When a harness changes
  its logs" in docs/reference.md (`doctor`, `internal/spec`, the shapes harvest).
- **Output is bounded.** Every view is sized for an agent's context. More detail
  is reached by drilling down (`#N`, ranges, `--full`), never by default.
- **No dependencies.** It's pure Go, with no database, daemon or index between it and
  the logs.
- **No TUI plans.** The owner has their own ideas for a TUI, so don't plan it,
  design it or write docs about it.
- **Copilot is low priority for now.** Keep it from breaking, but don't chase
  Copilot features.

## Build, test, install

```sh
GOCACHE=$TMPDIR/gocache go test ./...
GOCACHE=$TMPDIR/gocache scripts/coverage.sh --check
GOCACHE=$TMPDIR/gocache go build -o bin/agentlog ./cmd/agentlog
GOCACHE=$TMPDIR/gocache go build -o bin/agentlog-usage ./cmd/agentlog-usage
```

`~/bin/agentlog` and `~/bin/agentlog-usage` are symlinks to `bin/` here, so
rebuilding here is installing. Never copy a binary into `~/bin`.

Sandboxed agents can write only to the project and `$TMPDIR`. Anything
agentlog writes on its own (run records) has to keep working under
that restriction.

Work on main and split commits by concern. `docs/reference.md` is the full
reference and gets updated in the same commit as the behaviour it describes;
`DESIGN.md` gets updated when a decision changes.
