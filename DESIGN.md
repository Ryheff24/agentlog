# Design decisions

Most of what looks odd about agentlog was decided on purpose, after watching
agents use it and use the alternatives. Each section says what the decision
is, what was seen that led to it, and what it means in practice. If you're
about to "fix" one of these, read its reason first.

## Agents are the users

agentlog is built for agents, and agents build it. Run it bare and you'll see
a screen no person would choose to read: dense, abbreviated, full of refs and
follow-up commands. That's deliberate. The person who started it tried to use
it directly and found it too dense, and that was the right outcome.

**Why.** The human flow and the agent flow are different, and when they
conflict the agent's wins. An agent pays for every byte it reads and every call
it makes; a person pays in attention. Designing for one makes the tool worse
for the other.

**In practice.** Every change is judged by whether it makes an agent's work
cheaper: fewer calls, fewer bytes into context, less waiting. A human interface
(a TUI) is wanted someday, as a separate frontend over the same engine. That's
why `internal/engine` never looks at rendered text.

## Every argument is a clue

There's no subcommand grammar to learn. A harness name, a directory, an id
prefix, a time, a date, a file path or a word from the task can go in any
order, and agentlog finds the best match.

**Why.** Watching agents look for another agent's log, the pattern repeated:
locate the harness's log directory, find the recent session, get an overview,
then dig. Each step was a separate guess, and the digging was done the same
way every time: a heredoc or a one-off parser, written from scratch, every
day, at every handoff, for logs the last agent had already parsed its own
way. And an agent's first call to an
unfamiliar CLI is usually `--help`, which is a wasted call. Any detail the
agent already has (a project name, "it was Codex", "two hours ago") narrows the
search, so the tool should take all of it at once.

**In practice.** Agents should almost never run it bare or start with
`--help`. The snippet in the README exists so they don't have to. When one
does, it isn't wasted: `--help` prints the same screen as a bare call, the
running sessions and every flag, so it never needs both. When several
sessions match a naming clue equally well, the answer is the list, not the
newest one previewed as though it were the one meant.

## No token or turn budget

Views are bounded by default: a preview, a timeline of one-line events, long
fields clipped at `--max` bytes (6000 by default). But there's no cumulative
budget. agentlog never stops an agent from reading more, never caps how many
events or sessions a view covers, and `--full` turns clipping off entirely,
JSON included.

**Why.** An early design capped how much a session could pull, sized around a
20k-token target. That's small for the work agents actually get sent to do.
Without agentlog, agents were seen spending 200k to 300k tokens digging through
logs with one-off parsers, and a lot of that wasn't waste: it was the raw
detail they were reasoning with. When the job is "read every prompt the owner
has written in this project since it started," the large read *is* the job.
That's how an agent comes to understand what you're thinking. A single 10k to
20k token answer can also beat any number of small tool calls that each cost a
round trip.

**In practice.** If you document or review agentlog's usage and think "that's
way too many tokens," check what the call was reading first. Bounded by
default, unlimited on request: drill down with `#N`, ranges, `--full` and
`--json --full`, never by default.

Whether this is the optimal strategy is fair to argue. The defaults weren't
derived from a model of token cost. They are where thousands of calls a day,
asking the same kinds of questions over and over, settled: what an agent
needed from its first call often enough that a smaller default only bought a
second call. Change them on the same kind of evidence: `agentlog-usage`
measures calls, bytes and waiting per question (see
[docs/reference.md](docs/reference.md#measuring-what-it-costs)).

There's also a recursive cost. Everything agentlog prints lands in the calling
agent's own log, and that log is what later agentlog calls read. A larger
default isn't paid for once, in tokens: it is read again, as bytes to scan and
CPU to spend, by every later search that covers that session. The more agents
use agentlog, the more of the logs is agentlog's own output. Marking it
`[handback]` keeps it out of `--grep` matches, but not out of what has to be
read. Bounded defaults keep that loop small.

## Formats change every day

agentlog assumes Anthropic, OpenAI and GitHub change their log formats daily,
without notice. It has to degrade when that happens, never break and never go
silent.

**Why.** This is the make-or-break requirement. If keeping agentlog working
means sending an agent out to rediscover how the logs look every week, the
tool is useless. That's exactly the treadmill it was built to end. The worst
failure is the silent one: a reader that quietly stops understanding part of a
session and keeps answering questions about it.

**In practice.** A record this build can't place still becomes an event, read
by its shape, and every view says when records couldn't be read. `agentlog
doctor` says which types, from which CLI versions, and gives a ref to one. A
type can be named in `~/.config/agentlog/types.json` without a rebuild. The
shapes harvest keeps one redacted example of every record shape ever seen and
replays them all on every test run. Details are in
[docs/reference.md](docs/reference.md#when-a-harness-changes-its-logs).

## Read the files, nothing else

agentlog reads the harnesses' log files directly. No database, no daemon, no
index to build or keep fresh, and no dependencies outside the Go standard
library.

**Why.** Anything between the logs and the answer is something else that can
go stale or break when a format moves. A raw-byte prefilter turns out to be
fast enough without an index: looking up which sessions touched a file scans
785 sessions and 2GB in about a second.

**In practice.** Searching matches what a person typed, not what the log
stored. The prefilter allows for the escaping and wrapping harnesses apply to
text, so a sentence someone remembers is still found.

## One share of the machine

agentlog runs on the computer someone is working on, started by an agent, not
by them. It takes at most half the cores, capped at four, at lowered priority,
and every part of it draws from that one pool. Parallel runs share it too:
four agentlog calls at once aren't four times the machine.

**Why.** An early build ran its work in parallel with no overall limit, pinned
every core and ran a laptop hot enough that the process had to be killed.

**In practice.** `--jobs N` raises the limit, and agents are told not to touch
it unless the person asks for more speed. A raised limit is printed on stderr,
so it shows up in the transcript.

## Nothing hides its relatives

A parent can read its subagents, and a subagent can read its parent. Nothing
in the parent–child tree is hidden from anything else in it. The one exception
is the caller's own session, which is left out of searches (it would match
everything) unless named by id, and is marked "your own session" wherever it
appears.

**Why.** Parents read their children's work, and children read their parent's
instructions, all the time. Hiding either one breaks real workflows.

**In practice.** Claude Code currently breaks this rule for subagents. See the
first open entry in [ISSUES.md](ISSUES.md).

## The owner's words are separable

`--user` selects only what a human typed: no subagent spawn prompts, no
messages between agents, no injected instructions, task notifications or
compaction summaries.

**Why.** In an agent-heavy workflow, the human's turns are the spec. They are
the decisions, corrections and intent, scattered across hundreds of sessions.
Once harness and agent messages are mixed in, it's impossible to tell who
actually decided something.

**In practice.** `agentlog <session> --user`, run over each session in a project, is the
fastest way for an agent to learn how the person thinks about
a project. Older logs don't always record whether text was pasted or typed, or
whether a prompt came from a person or an orchestrator. agentlog uses what's
recorded and doesn't guess at what isn't.

## Tested the way an agent would use it

Every change gets unit tests of real behaviour, and then an end-to-end test:
a real question, answered on real logs using only agentlog, and checked
against ground truth (the raw JSONL, git history, file mtimes), never against
agentlog itself.

**Why.** Early on, most of the reported bugs were easy ones, which is the sign
of no real testing. End-to-end command tests counted as coverage while barely
touching the code. And a question like "what have I been working on recently,
and where did I leave off?" can come back wrong in ways no unit test catches:
too far back, too recent, missing the second or third project, or forgetting
the subagents that actually did the work.

**In practice.** Coverage is held at 95% by tests of real behaviour, and code
no real case reaches is a candidate for deletion, not for a test written just
to run it. See [CLAUDE.md](CLAUDE.md) for the loop.

## Control bytes are named, binary is held back

Logged text reaches an agent as text. A control character is printed as the
character that names it (`␛`, `␀`, `␇`), and a field that is mostly binary is
replaced by one line with its size, its counts, and the `--bytes` command
that prints it as logged. This holds for a pipe as much as a terminal, and
for JSON as much as text.

**Why.** What agentlog prints lands in a model's context. A tool result that
is a compiled file read as text is a run of control bytes and U+FFFD: nothing
an agent can use, and close to the encoded data (base64, hex) that
Anthropic's guidance for some models says can trip safety classifiers.
Whether binary does is unverified; the one stop seen after such bytes came
from a one-off script, not agentlog. `internal/term` only ever escaped them
for a person's terminal, and agents read pipes.

**How it is chosen.**

- **One writer at the end.** Every byte written goes through `term.Visible`,
  and JSON through `term.JSON`, so a path nobody thought of (a title, a
  doctor sample, a plan step) is covered too. The stand-in is decided where
  the field is known, so it can name the event.
- **Pictures, not `\xNN`.** A control picture is one character and does not
  read as hex; `\x1b[31m` repeated through a binary dump is exactly the dense
  hex-like run to avoid. C1 controls have no pictures and print as `\u0085`;
  they are rare in text.
- **Escapes for a few, a stand-in for many.** Across one Mac's Claude and
  Codex logs, 651 distinct strings held a control or U+FFFD character. Most
  held one or a few in long text. Of the 34 at 2% or more, most were binary
  reads with tens to thousands, of many kinds, but some were readable:
  backspaced output (only backspaces), form-fed output and UTF-8 text decoded
  as Latin-1 (only C1).
  So the rule counts U+FFFD, NUL, DEL and the C0 controls that do not shape
  text (not newline, tab, CR, ESC, backspace, form feed, vertical tab), and
  needs 16 or more of at least three kinds, at 2% or more of the whole field
  or of what the clip keeps. On that Mac 19 strings pass it, each with 3 to
  27 kinds. The 8 a plainer count would also catch print with each character
  named: the readable ones above, a record list separated by SOH, and two
  texts of only U+FFFD (whether those were binary is unknown).
- **Nothing becomes unreachable.** `--bytes` prints the text as logged,
  and `--bytes --json` keeps every string's exact value as escapes. A
  terminal still gets `term.Safe`'s escaping either way.

**Rejected.**

- **Escaping only.** It turns 128 control bytes into 128 escapes in the same
  place: smaller harm, same noise.
- **A stand-in only.** A coloured test log would lose its text over a few
  escape codes.
- **Stripping ANSI colour codes.** Cleaner, but it drops what was logged
  where naming it costs one character each.
- **Treating long hex and base64 as binary.** Commit hashes, checksums,
  session ids and lockfile integrity lines are common and legitimate; see
  [IDEAS.md](IDEAS.md).

## Small things that were decided

- **Rate limits carry their time.** A percentage read four days ago is not the
  current one, so `--usage` never presents it as current.
- **agentlog's own output is not evidence.** Earlier agentlog output inside a
  transcript is marked `[handback]` and left out of `--grep` by default, so
  searching for something doesn't keep finding past searches for it.
- **Missing data is said, not invented.** "No tokens recorded" rather than a
  zero; "working-tree state unknown" rather than a guess.
- **Images go to files.** `--images` writes inline images to temp files and
  never prints base64 into a context.
