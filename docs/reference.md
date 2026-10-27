# agentlog reference

The full behaviour of every clue, view and flag. [README.md](../README.md) covers
why agentlog exists and how to point agents at it; [DESIGN.md](../DESIGN.md) covers
why it behaves the way it does.

## Clues

Every argument is a clue. Clues can go in any order and all of them are
optional.

| Clue | Meaning |
|---|---|
| `codex`, `claude`, `copilot` | harness |
| `.`, `~/path`, `my-project` | working directory (exact, subdir or parent) or project name |
| `3f9c2e1a`, `codex://threads/<id>`, `agent-<id>` | session id prefix: opens that session |
| `2h`, `3d`, `today`, `all` | recency (default last 7 days; a widened window is always announced) |
| `2026-09-09`, `sep9`, `9/9`, `9sep` | one day: sessions that were running on it |
| any other word | matched against project, title, prompts, last reply and subagent names |
| `src/App.tsx`, `/abs/path.go` | a file: which sessions changed or mentioned it |
| `<ref>#N`, `<ref>#N-M`, `<ref>#N-` | one event, a range, or everything from there on, in full |

```sh
agentlog                          # harnesses + recent sessions
agentlog codex .                  # newest Codex session here, previewed
agentlog codex://threads/3f9c2e1a-7b40-4d8e-9a21-5c6e0b7d4f12
agentlog 3f9c2e1a-7b40#85         # event 85 of that session in full
agentlog codex . --edits --files  # files changed, including by subagents
agentlog 3f9c2e1a-7b40 --usage    # tokens, cost and rate limits at the time
agentlog 3f9c2e1a-7b40 --tools    # which tools it used, how often, how much output
agentlog 8a1d0c77 --user          # the person's turns, excluding harness/agent messages
agentlog 8a1d0c77#1- --kind owner --branches --full  # human turns including rewound branches
agentlog all needle --content --count  # event-content matches and session counts
agentlog 8a1d0c77#42 --images     # extract inline images to writable temp files
agentlog 8a1d0c77 --tree          # sourced spawn relationships
agentlog claude my-project --list         # candidates instead of the best match
agentlog src/analytics/data.go all  # every session that touched that file
```

## Views

The bare home screen puts running sessions across all projects first, including
parents with running subagents. Its default 12-row preview expands to include
all of that running work, followed by completed sessions in the current
directory and then elsewhere. An explicit `--limit N` still caps the output.
Named lookups keep their relevance ranking; `.` explicitly scopes a lookup to
the current directory.

The preview includes the following:

- where and when the session ran, and how it ended (done, usage limit,
  interrupted, error, mid-tool, still working)
- event counts, and the busiest tools
- what it committed, and on which branch
- what it spent, when the harness wrote it down
- the first and latest prompts
- subagents and their state
- files changed
- the last events
- the final assistant message

It ends with the commands for the next step. Paged selections retain their
kind, grep, branch and subagent filters in continuation hints. Head pages
continue forward; tail pages continue backward, with no repeated event at
the boundary. The output format and page size carry through too.

View flags apply to the best match: `--tail N`, `--head N`,
`--kind user,assistant,tool,edit,agent,plan,error,notice,reasoning`,
`--grep REGEX`, `--edits [--files]`, `--usage`, `--tools`, `--subagents`,
`--branches`, `--list`,
`--json`, `--full` and `--max BYTES`. When several sessions match and no id
was given, `--grep` searches across them.

`--user` (alias for `--kind owner`) selects human prompts for all three
harnesses. It excludes subagent spawn prompts, peer and teammate messages,
task notifications, compaction summaries, and injected instructions or command
expansions. It composes with event ranges, `--grep`, `--head`, `--tail`,
`--branches`, `--full` and `--json`. Pasted text stays in; `[pasted]` and JSON
`pasted` mark pastes/attachments when the harness records a paste marker.
Older logs may not distinguish a paste from typed text, or an unmarked
orchestrator prompt from a human prompt; agentlog uses recorded origin flags,
wrapper shapes and session linkage, and cannot recover provenance absent from
the log. `--kind user` remains the broader user-role view.

`--kind refusal` selects refusal errors and preserves recorded category and
explanation. `--content` searches word clues in event content rather than
project/title/prompts; `--grep` remains the regular-expression form. `--count`
prints matching event/session counts before head/tail paging. `--brief` prints
one line per event, at most 160 characters. Text views report returned stdout
bytes on stderr; JSON keeps its typed schema and run monitoring measures bytes.

Historical output from standalone agentlog calls is marked
`[handback]`. Grep excludes it unless `--include-handbacks` is supplied;
expanded tool results collapse it to command provenance and byte count, with
`#N --full` to retrieve the body. This prevents recursive transcript hits from
posing as work done in the containing session. Inter-agent messages and
SendMessage calls are real session content and remain searchable by default.

`--images` lists local paths and remote URLs, and decodes inline images into
private files in the writable temp directory. It never prints base64 or fetches
remote URLs. Event refs, source file and line identify the record, including
queued Claude attachments and separate Codex pages/tool results. The caller
may remove the temporary image files when finished. Several explicit ids export
all selected sessions; `--subagents` includes their children. Every image
carries its session ref, including in the combined JSON array.

`--tree` shows recorded parent/descendant linkage, omitting the name column
when no subagent name was recorded. Copilot inline task runs appear with their
parent-session event refs; they are labelled "inline task" because they have
no separate session file. Nested runs requested with `--subagents` retain
that flag in their refs. A directory with
`--edits --files` includes matching sessions and reports each file's last
recorded successful writer. Copilot MCP `filesystem-edit_file` and
`filesystem-write_file` calls contribute their recorded edits too; dry-run
previews and failed calls never become successful writers. A file clue with those flags selects edit-only
sessions, newest writer first. Previews show last recorded commit/push and
later edit evidence; working-tree state stays unknown without a logged status
snapshot. Lists and doctor explain that no-rollout ephemeral jobs and logs
outside the harness roots are outside the catalog. Doctor gives each unread
type its observed CLI versions, bounded to two plus a remainder count. It
honors time clues and the session limit: use `doctor copilot all --limit 100`
to include older CLI logs alongside recent agent-host logs that may record
version `0.0.0`.

When several sessions match a naming clue — a word, an id prefix — equally
well, the answer is the list of them, not the newest one previewed as though
it were the one meant. Clues that only narrow the field (a harness, a
directory, a day) still open the best match, since recency is a real reason to
prefer one.

A file as a clue asks a different question: not which session to open, but
which sessions touched this file. Candidates come from the same lookup as any
other clue, the basename is scanned for over the raw bytes, and only the logs
that contain it are parsed — 785 sessions and 2GB in about a second, with no
index to build or keep fresh. A change outranks a mention, and a file found in
another worktree is listed with the path that was actually touched. It works
on a file that has since been deleted or renamed.

`--tools` is the census: every tool the session called, how often, how often
that failed, how much output it poured into the context, and the event range
to look at. It counts a tool call whatever the parser made of it, so subagent
spawns and plan writes are in it too.

`--usage` is what the session cost: tokens split by fresh, cached and output,
money when the harness records it, which models it went to, and the rate-limit
readings with the time each was taken — a percentage from four days ago is not
the current one, so the view never presents it as one. Codex writes a running
total per turn, Claude Code writes per-response counts plus its own cost
account. Copilot CLI records per-model input, output, cached tokens and request
counts at shutdown. Older records reset those metrics on resume and are
summed by segment; newer records with `tokenDetails` restore cumulative
metrics, so the latest nonempty snapshot wins. Empty or repeated shutdowns
do not erase or double-count usage. Copilot input includes cached tokens;
the view subtracts those to report fresh input. A log without usage still
says "no tokens recorded".

`--subagents` adds the subagents' own work: their sessions for Claude Code and
Codex, and for Copilot CLI the steps it logs inline in the parent's file, each
marked with the subagent that ran it. Without it their step count is reported
in a note. `--branches` adds the turns a session rewound, edited or restarted;
without it they are hidden and counted. Timeline, tool census, usage and edit
views all report the rewound turns they left out. Lifetime cost records remain
the harness's own accounting, including work it later rewound.

A search matches what a person typed, not what the log stored. Their words are
written into the log escaped and wrapped, so a sentence they remember is bytes
that no grep for the sentence will find; searching for `UPDATE users SET name`
over this corpus returned "no log contains it" while the corpus contained it
eight times. The raw-byte prefilter looks for the longest run of the pattern
that escaping cannot have broken, and the match itself is made against
whitespace-folded text.

Views have no cumulative token or session budget. Individual expanded
messages, tool outputs and diffs are clipped at `--max` bytes (6000 by
default) in both text and JSON, and `--full` disables that clipping. `--json` emits a typed response
— the session, the events, the counts behind them, what was truncated and what
to run next — never the raw transcript.
`--json --full` also disables event-field clipping. Tool inputs remain the
reader's normalized representation rather than a raw JSON record.

The calling agent's own session is left out unless it is named by id, using
`CLAUDE_CODE_SESSION_ID` and `CODEX_THREAD_ID`. When it is the only session
that matches, the output says "no sessions but your own" and gives the id that
opens it. Wherever it is shown (opened, listed by id, `--tree`), it is marked
"(your own session)". Claude subagents can inherit the parent's id without
an independent child-id environment signal; this known limitation is tracked
in [ISSUES.md](../ISSUES.md). Name the parent explicitly to read it in that situation.

A Claude session that carried on in another directory can leave a log under
each project folder. They are read as one session: listed once, with the
subagents from every folder, its conversation read oldest file first with
records repeated by a cross-file copy shown once, and every file named in the header.
Within a file, later records with the same UUID and timestamp are hidden from
ordinary views and counts, with an "N repeated records hidden" note. The first
copy supplies the visible content; directory and branch history still uses
recorded metadata. Copies retain their existing event numbers: an explicit
`#N` or range opens them and marks each as "repeat of #M". Cross-file copies
that were already merged do not acquire new event numbers.
A directory clue matches any directory it worked in, and the `dir` line says
where it started.

## When a harness changes its logs

The three harnesses change their formats without telling anyone, roughly
weekly, and the failure that costs most is the silent one: a reader that
quietly stops understanding part of a session and goes on answering questions
about it. Four things make that visible instead.

A record this build cannot place still becomes an event — in position, with
its own `#N`, carrying its content. It is read by shape rather than by name,
so a tool call whose record type was renamed still arrives as a tool call with
its command and its output; anything less readable is flattened to
`field: value` lines. Fidelity degrades; the record is never dropped. The same
holds for a result whose call cannot be found: it is shown on its own rather
than hidden.

Every view ends with a note when the session held records this build could not
read. `agentlog doctor` is the report behind that note: per harness, the CLI
versions found in the logs, how much was read, and for every unreadable type
its count, the sessions it appeared in, the exact ref that opens one, and what
that record contains — enough to write the missing case without opening a raw
log.

Each reader also states what it knows as a table (`internal/spec`), checked
against the reader by a test, so a fix is usually one line. A record type can
be named without a rebuild in `~/.config/agentlog/types.json`:

```json
{"copilot": [{"type": "session.warning", "role": "step", "note": "MCP warnings"}]}
```

`go test ./internal/shapes -harvest` reads the logs on this machine and keeps
one redacted example of every record shape it has ever seen; every one of them
is replayed through its reader on every test run, so a provider moving a field
shows up as a changed golden rather than as a discovery. The fixtures carry
the shape and nobody's words: strings become placeholders, paths become
`/Users/me/project/file`, ids become hashes.

## What it takes from the machine

This runs on the machine somebody is working on, started by an agent rather
than by them, so there is **one** limit on how much of it agentlog may take:
half the cores, at most four. Search, `doctor`, the file lookup and enrichment
all draw workers from that one pool rather than each keeping their own —
limits counted separately are limits that stack — and `GOMAXPROCS` is set to
the same number, at a lowered scheduling priority.

    --jobs N   |   AGENTLOG_JOBS=N

**Agents: do not raise `--jobs` on your own initiative.** Raise it only when
the person asks for more speed. A raised limit prints the value in use on
stderr, so it is visible in the transcript.

Parallel invocations share the same limit: each run records what it took in
the cache directory and the next one takes what is left, down to a single
worker, so four agentlog calls at once are not four times the machine. Claims
are released at exit and ignored once their process is gone.

A cross-session search reads every candidate; one matching session never
crowds later matches out of the answer.

    all --grep pgvector    1980MB read, 5.7 CPU-s  ->  272MB, 3.3 CPU-s

## Measuring what it costs

Two measures, kept apart so one breaking does not blind the other.

**agentlog's own record.** Every run appends one JSON line: its arguments,
the calling session (from `CLAUDE_CODE_SESSION_ID` / `CODEX_THREAD_ID`), wall
time, time in each phase (discover, enrich, load, scan), and the bytes it
printed, which for an agent is what entered its context. It goes to the first
of `~/.config/agentlog/runs.jsonl`, `$TMPDIR/agentlog-runs-<uid>/runs.jsonl`
or `/tmp/agentlog-runs-<uid>/runs.jsonl` that can be written: an agent's
sandbox usually allows only its temp directory. Each file stays under 8MB.
`AGENTLOG_RUNS=path` sets the file; `AGENTLOG_NO_MONITOR=1` turns it off.

**`agentlog-usage`**, a separate command, reads the harnesses' session logs
itself, not through agentlog's readers, for every shell call an agent made to
agentlog:

    go build -o bin/agentlog-usage ./cmd/agentlog-usage
    agentlog-usage [7d | 24h | all] [--top N] [--json]

Usage calls are deduplicated by harness plus tool invocation id, and Codex
fork replay is excluded. The default `--callers` view excludes development
sessions (recorded edits to agentlog); `--all-callers` retains
them for audit. Starting in the agentlog directory alone does not make a session development.
Only executable command positions count as invocations: quoted mentions,
grep patterns, comments and heredoc data do not. Shell wrappers and Codex
exec scripts retain their actual nested invocations.

It reports latency at each level, from the outside in: how long the agent
waited for the call; how long the harness says the command ran (Codex logs
its own wall time); how long agentlog ran, from its record of the run; the
difference, which is the shell, the sandbox and any approval prompt; and how
long the model took to its next step after reading the result. Then the bytes
each call put into context, by view, by session (against everything that
session's tools returned) and the heaviest and slowest calls. Only calls that
ran agentlog alone (piped through `head` and the like) are measured; a
command that also built or grepped would count that work as agentlog's.

## Tests and coverage

`go test ./...` runs everything. `scripts/coverage.sh` prints statement
coverage per package, counted across the whole suite, and `coverage.txt` is
the recorded baseline. Run `scripts/coverage.sh --check` before committing: it
fails if the total or any package fell below the baseline. After adding tests,
`scripts/coverage.sh --update` records the new numbers. The goal is 95%, reached
with tests of real behaviour: a line that no real case reaches is a candidate
for deletion, not for a test written only to run it.

## Layout

| Package | Purpose |
|---|---|
| `internal/transcript` | provider-neutral session and event model |
| `internal/codex`, `internal/claude`, `internal/copilot` | discovery and parsing per harness |
| `internal/catalog` | clue parsing, discovery across providers, ranking |
| `internal/engine` | filtering, selection, typed responses — no rendered text |
| `internal/scan` | raw-byte search over whole logs, in front of parsing |
| `internal/generic`, `internal/flat` | reading a record by shape; flattening any JSON to text |
| `internal/spec`, `internal/shapes` | what each reader knows; fixtures of every record shape seen |
| `internal/render` | home, list, preview, timeline, event and edit views |
| `internal/app` | argument handling and view selection |
| `internal/clip`, `internal/patch`, `internal/jsonl` | text shortening, edit normalisation, bounded JSONL reads |
| `internal/monitor` | each run's record of its own time, phases and output |
| `internal/usage` | `agentlog-usage`: agents' calls to agentlog, read from the harness logs |

`internal/engine` answers questions about events; `internal/render` turns an
answer into text. Nothing in the engine may look at rendered output, so a
second frontend can be built on the same answers.
