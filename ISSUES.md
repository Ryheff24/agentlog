# Known issues

Problems in agentlog that have been reproduced and are not fixed yet. An entry
goes in when a problem is reproduced against real logs, and comes out in the
commit that fixes it; that commit is the record.

## Open

- **New record types this build can't read yet.** Found by
  `TestTodaysLogsAreRead` on 2026-10-27, after the harnesses' latest releases:
  Codex 0.164.0 `response_item:additional_tools`; Copilot CLI 1.0.98
  `session.usage_record` (per-call tokens, cost and model),
  `subagent.configured` (a subagent's model and effort) and
  `skill.context_delivered_ref`. They show as generic events with a note
  pointing at `agentlog doctor`. Some carry data a view should use
  (`session.usage_record` for `--usage`), so each needs reading, not a blanket
  ignore.

- **`agentlog-usage` is not wired into agentlog.** It's a second program with
  its own log reader, built only from source; the releases, install.sh and
  agentlog's help leave it out. It will likely become a subcommand, but folding
  it in has to keep its reader separate from agentlog's (see
  [docs/reference.md](docs/reference.md#measuring-what-it-costs)), so it waits
  for that to be done properly. Its check for a session working on agentlog
  itself still looks for `module agentlog` in `go.mod`, which the module
  rename to `github.com/Ryheff24/agentlog` broke; the path-name fallbacks still
  catch a checkout named `agentlog`.

- **A Claude subagent can hide its parent.** Claude Code subagents inherit the
  parent's `CLAUDE_CODE_SESSION_ID`, so when a subagent runs agentlog, the
  parent is treated as the caller's own session: left out of lists and marked
  "your own session" when opened. That breaks a rule agentlog holds (see
  DESIGN.md, "Nothing hides its relatives"). The log itself knows the
  difference, since subagents get their own `agentId` and file, but the
  environment the caller runs in does not expose it, and `CHILD_SESSION=1`
  also shows up in top-level sessions, so it can't be the signal. Waiting on a
  decision about which identity signal to trust.
  `TestInheritedClaudeIdentityMustNotHideParent` reproduces it and is skipped
  until then. Workaround: name the parent by id.

- **The live-format test fails when the harnesses ship new record types.**
  `go test ./internal/shapes -run TestTodaysLogsAreRead` reads today's logs on
  the machine running it and fails on any record shape this build has never
  seen. At the time of writing that is Copilot CLI's `session.usage_record`,
  `skill.context_delivered_ref` and `subagent.configured`. Those records still
  become generic events, so nothing is lost in a view, but the gate is red
  until each is named. This one will never stay fixed: it is the alarm
  described in DESIGN.md, "Formats change every day".

- **`--content` ignores `--grep`.** With a word clue and `--content`, the
  regex has no effect. `agentlog record --content --grep zzqqxx_nomatch
  --count all` reports 195,022 events in 3,367 sessions. The same command
  without `--content` reports 0. An agent that adds `--content` to narrow a
  search gets every event that holds the word instead. Found on 2 November 2026.

- **Prompts that a program wrote count as the person's.** On one Mac, 515 of
  754 top-level Claude sessions came from a per-commit security-review hook
  that starts Claude through the SDK. Their first prompt is "Review this
  change for security vulnerabilities". `--user`, documented as "what the
  person typed", returns those prompts. In `agentlog <project> --grep workbook
  --user all`, every hit was one of them. The same sessions rank first in
  lists, ahead of the session that worked on the branch. The log marks some
  of them: 326 carry `entrypoint: sdk-py`, and sessions a person started
  carry `cli`. But 192 hook sessions have no `entrypoint`, so that field
  alone does not identify all of them.

- **Workflow journals are not read.** The Workflow tool writes
  `subagents/workflows/wf_*/journal.jsonl` with `launched`, `started` and
  `result` records (`agentId`, `key`, `toolsUsed`, a structured `result`).
  The shapes test reads them as `unread=1`. Opening one by path fails:
  `agentlog …/journal.jsonl --tail 5` widens to all time and reports "no
  session matches file=…", because the path goes to the file-clue search, not
  to the log reader. Agents read these journals by hand in 8 episodes across
  38 days, to follow a running workflow.

- **A wide subagent search is not bounded.** `agentlog <id> --subagents
  --grep '<a name>' --brief` returned 54.6KB: 74 hits, most from the parent
  session. A count for each session first, with `#N` to drill down, would
  keep it within an agent's context.

- **`agentlog-usage all` prints the wrong start.** It prints "since Dec 31
  23:58", the zero time in local time, not the date of the oldest log.

## Not acting on

Reports that were read, checked, and deliberately left alone, with the reason.

- **Skipping a home screen already printed in the same command.** Agents chain
  `agentlog --help && agentlog`, and since `--help` is the home screen the
  second call repeats it. Spotting that needs to know which calls belong to
  one command line, and nothing says so reliably. The process session id
  (`getsid`) grouped one Claude Code tool call on macOS, but that was the only
  case checked: Codex, Copilot CLI, Linux, other shells and future harness
  releases can each start commands differently and merge or split calls.
  Harness session ids are per session, not per command, and subagents can
  inherit the parent's. The logs don't hold the running call yet. A wrong
  guess tells an agent it already saw help it never saw, silently; a repeat
  only costs characters. Don't build it until every harness, OS and shell is
  checked and can be re-checked when they change.

- **Copilot caller self-exclusion.** No local Copilot CLI session-id variable
  could be reproduced in the logs. `COPILOT_AGENT_SESSION_ID` is cloud-agent
  configuration, not the local caller's identity. agentlog won't invent a
  variable or borrow a cloud identity for local logs.
- **JSON self-exclusion.** With a caller's session id set, a JSON list omits
  that session and says so; naming the id includes it with `self=true`. That
  matches the text views and the documented rule. A tool that wants to count
  its own work should ask for its own id explicitly.
- **Copilot `doctor` reporting version `0.0.0`.** Those logs really record
  `0.0.0`: they come from the VS Code agent host. `doctor copilot all
  --limit 100` shows the CLI's real versions alongside them.
- **A path no session touched, with a time clue and `--list`.** Already reports
  no session; the earlier report of unrelated sessions didn't reproduce.
- **Pre-compaction access, range filters and branch notices.** Already covered
  by regression tests.
- **Signals the logs don't record.** Ephemeral-run persistence and PIDs, paste
  metadata where the harness writes none, Codex refusal shapes, `requestId`
  stability: unverified, so agentlog doesn't pretend to read them.
- **Clipping `--json --full`.** Proposed to keep JSON small. Rejected: `--full`
  means full, in JSON too.
- **A subagent log older than its parent.** A list counts it and opening the
  parent leaves it out. Claude Code always writes a subagent after its parent
  starts, so only a forged mtime produces this.
- **A later file reusing a record's uuid with different text.** The list shows
  the later text as the last prompt while the opened session keeps the first
  copy. The first copy wins by rule, and keeping it that way keeps existing
  refs stable. UUID reuse is not assumed impossible.
- **A bare search that widens to all time.** When only the caller's own session
  is recent, the search widens and says so, then says there are no sessions but
  your own. That's the search doing its job: older sessions in the directory
  are what's wanted then.
