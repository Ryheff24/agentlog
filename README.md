# agentlog

A command-line tool for reading Claude Code, Codex and GitHub Copilot CLI
session logs: find another agent's session, see what it ran, changed and
spent, and search across all of them. Read-only, no network, pure Go.

Agents need to know what another agent did: what it ran, what it
changed, where it stopped, what it cost. The answer is already on disk, in the
session logs Claude Code and Codex/ChatGPT write. But those logs are raw
JSONL, shaped differently by every harness and reshaped whenever one ships. So
agents kept writing one-off heredocs and parsers to dig through them, paying
context and time on every attempt. agentlog started when an agent had to
recover a Codex thread that ran out of limits mid-fix.

agentlog is an attempt at a parser, written once. An agent hands it whatever it knows (a
directory, a harness, a session id, a time, a word from the task) and gets back
a bounded overview of the best match, with the follow-up commands to drill into
events, edits, subagents or a grep.

Agents are the users, and agents build it too. Run `agentlog` bare and you'll
see it isn't made for a person to read; that's on purpose. I tried it
and found it too dense. Every change is judged by one question: how little
context and time can an agent spend finding out what another agent did?

## It only reads

agentlog never writes to, moves or deletes the harness logs, makes no network
connections and runs no other programs: nothing it reads leaves your machine.
A test fails the build if either binary ever links networking or process
code. The only files it writes are its own, readable by you alone (`0600` in
`0700` directories), and never through a link someone else placed:

- **A run log.** One JSON line per call (arguments, working directory, the
  calling session's id, duration, bytes printed) in
  `~/.config/agentlog/runs.jsonl`, or a temp directory when a sandbox blocks
  that. It stays local; `agentlog-usage` reads it. `AGENTLOG_NO_MONITOR=1`
  turns it off.
- **CPU claims.** While it runs, one small file per process in your user
  cache directory, so parallel runs share one limit. Removed on exit.
- **Images, only with `--images`.** Written to temp files instead of printed.

**What it prints is not safe to trust.** Session logs hold everything that
passed through a session: secrets someone pasted or a command printed, and
text from web pages, files and tool output that anyone could have written.
agentlog prints that into whatever reads it. If an agent reads it, the agent
should treat it as data, not instructions, and the output shouldn't go
anywhere you wouldn't put the logs themselves. In a terminal, control
characters are shown as `\x1b` and the like instead of being acted on; through
a pipe, an agent gets the bytes as they are.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/Ryheff24/agentlog/main/install.sh | sh
```

That downloads the prebuilt binary for your machine from the latest
[release](https://github.com/Ryheff24/agentlog/releases) (macOS universal,
Linux amd64 or arm64), checks it against the release's checksums, and puts it
in `~/.local/bin`. On macOS it also requires the binary to be signed with the
maintainer's Developer ID and notarized by Apple. That signature is made on
the maintainer's Mac, not on GitHub, so a compromised repository can't fake
it. `AGENTLOG_INSTALL_DIR` changes where it installs, and
`AGENTLOG_VERSION=v0.1.0` pins a version. `agentlog --version` says what you
have. Read [install.sh](install.sh) first if you'd rather not pipe a script
into `sh`; it's short.

To check a download by hand, every archive comes with a signed record of the
commit and workflow run that built it (on macOS, the record covers the
unsigned build; the signature covers the rest):

```sh
gh attestation verify agentlog-linux-amd64.tar.gz -R Ryheff24/agentlog
codesign -dv --verbose=2 ~/.local/bin/agentlog   # macOS: look for the TeamIdentifier
```

The Linux builds are reproducible: `scripts/release.sh` run on the tag with
the same Go version (`go version -m agentlog` shows which) gives the same
bytes.

With Go 1.26 or later, you can instead run:

```sh
go install github.com/Ryheff24/agentlog/cmd/agentlog@latest
```

The repository also holds `agentlog-usage`, a maintainer's tool that measures
how agents call agentlog. It is not wired into agentlog: it's a separate
program with its own log reader, it isn't in the releases or installed by
install.sh, and agentlog's help doesn't mention it. It will likely become an
agentlog subcommand, so its interface may change. See
[docs/reference.md](docs/reference.md#measuring-what-it-costs).

Pure Go, no dependencies. Developed and used on macOS. The Linux builds pass
the test suite but haven't been used on real Linux logs yet. If something
looks off there, run `agentlog doctor` first, then open an issue.

Then add the snippet below to your agent instructions.

## Tell your agents about it

Put this in a `CLAUDE.md` or `AGENTS.md`, so agents call agentlog directly
instead of starting with `--help`:

```md
To read another agent's session (Claude Code, Codex, Copilot CLI), run
`agentlog` with whatever you know as arguments, in any order: harness
(`codex`/`claude`/`copilot`), a dir (`.`) or project name, an id or
`codex://threads/…` link, a time (`2h`, `today`), or words from the task.
It prints a preview of the best match with follow-up commands. Use
`<ref>#N` for one event in full, `--user` for only what the person typed,
`--edits` for diffs, `--grep RE` to search, `--usage` for tokens, cost and
rate limits, and a file path to find every session that touched it.
Never parse the JSONL logs by hand. If a view ends with a note that records
could not be read, run `agentlog doctor`: it says what changed and where.
Do not pass `--jobs`: it decides how much of the person's machine to take,
and the default is deliberate. Raise it only if they ask for more speed.
```

## How to point an agent at it

The snippet teaches an agent how to call agentlog. It doesn't teach it what to
look for, and that's what decides whether it finds anything useful. That part
is on you, in how you prompt.

**Say what it's looking at.** Logs are history, and history contradicts
itself. Projects change direction, and your feedback changes with them. An
agent reading months of your turns will find complaints about things that
were fixed long ago, decisions you later reversed, and issues that may or may
not still be real. Tell it so. Something like:

> The project has changed a lot over time, and so has my feedback. Some issues
> you find will be about things that were fixed but never confirmed; others
> are still open. Check against the current code before you treat any of it as
> current.

A strong model given that perspective will reason across a large history and
work out what you actually want now. Without it, it takes old complaints at
face value.

**Say what it's for, and how far to go.** At one end, it's exact: build a spec
for a feature where every requirement is backed by a quote from your own
turns, with the ref that opens it. At the other end, it's open-ended: "go
through the history of this project and tell me what I keep running into."
Both work. The open-ended kind is where agentlog earns its keep, because
agents find things in logs you'd never think to search for. Be clear which one
you're asking for. 

**Send cheap agents wide, strong agents narrow.** The pattern I often use and
noticed works best in practice:

1. Small, fast models (Haiku, Luna, etc) do the raw reading: every user turn in a
   project, wide searches across hundreds of sessions, `--user` dumps, `--grep`
   sweeps. They're faster and cheaper, and they don't have to be brilliant to
   read.
2. They report back what they found, with refs (`3f9c2e1a#85`) and details to 
   properly scope a request.
3. A stronger model makes scoped calls for exactly those events and does the
   reasoning.

When a project is breaking down and the cause isn't obvious, skip step 1 and
let a strong model read the whole history itself. It costs more, and that's
when it's worth it. 

**Expect big reads.** agentlog has no token budget on purpose
([DESIGN.md](DESIGN.md#no-token-or-turn-budget)). A call that pulls every
prompt you've written in a project can be tens of thousands of tokens. That's
not waste; it's how the agent learns how you think.

Prompts that have worked:

- "I had a Codex agent working in this directory. It and its subagents were
  about to make a regression fix when the limits ran out. I need that fix
  delivered."
- "Tell me the most recent projects I'm working on and where I left off in
  each, across the whole machine." (A good test: the answer is wrong if it
  goes too far back, sticks only to the last day, misses the second or third
  project, or ignores the subagents that did the actual work.)
- "Read every turn I've written in this project and draft the spec for X.
  Quote me, with refs, for every requirement."
- "Use three small agents to read my turns and the agents' replies in these
  projects, then tell me where the work actually changed direction."

## What a call looks like

```sh
agentlog codex .                  # newest Codex session in this directory, previewed
agentlog codex://threads/3f9c2e1a-7b40-4d8e-9a21-5c6e0b7d4f12
agentlog 3f9c2e1a-7b40#85         # event 85 of that session, in full
agentlog 8a1d0c77 --user          # only what the person typed
agentlog my-project 30d --list    # every session in a project, a month back
agentlog codex . --edits --files  # files changed, including by subagents
agentlog 3f9c2e1a-7b40 --usage    # tokens, cost and rate limits at the time
agentlog src/analytics/data.go all  # every session that touched that file
agentlog all needle --content --count   # which sessions mention something, and how often
```

A preview says where and when the session ran and how it ended (done, out of
limits, interrupted, error, still working); what it committed and spent; its
first and latest prompts; its subagents and their state; the files it changed;
its last events and final message. It ends with the commands for the next
step.

The full list of clues, views and flags is in
[docs/reference.md](docs/reference.md).

## More

- [DESIGN.md](DESIGN.md): the decisions behind agentlog and what led to each.
- [docs/reference.md](docs/reference.md): every clue, view and flag; how
  format changes are handled; machine limits; cost measurement; package layout.
- [ISSUES.md](ISSUES.md): what's reproduced and not fixed yet, and what was
  deliberately left alone.
- [CLAUDE.md](CLAUDE.md): how agents work on agentlog itself.

## Notes from the owner
This tool I've been iterating on over roughly the past 6-9 months. Expanding the functionality more in
my opinion requires a full respecification of the tools surface, if interested I have many plans and iterations of such.

Finally, some easy wins to improve performance and accuracy include:
- Adding an embedding model. I have work planned for this, but upstream depedencies custom to my implementation have this on hold for me personally(An embed server that runs native to
Apple's ANE is what im working on for this. it keeps the CPU and GPU mostly free and the ANE is underutilized. the ane-embed-server part has been proven to work internally, but a conversion pipeline is unreliable at best)
- Lower level parser implementation logic, pre-load and fetching strategies. A lot of slow down
is I/O based, reading old transcripts and such from cold storage, 
- Rework the tools surface. I've had plans in the pipeline to change what currently exists in some form, i have many plans that reduce the total number of tools surfaces exposed in favor for denser
interactions in each surface. But I've held off because I think it will reduce some rather niche and less used functionalies. Things agents might have learned from use and rememebered through memory for example, but aren't exact specified use cases. 