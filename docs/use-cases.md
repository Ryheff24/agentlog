# Use cases

Real cases from my machine, read back out of the sessions themselves. In most
of them nobody asked for agentlog: the agent reached for it because the answer
existed nowhere else. Git keeps what the work produced. The session logs keep
the work itself: every command and its output, every file that passed through
`/tmp`, every word I typed, and when.

## A deleted file, recovered from another agent's curl

Two generated files in my main project were deleted. Git didn't track them.
The agent checked git, Time Machine snapshots, VS Code's local history and
`/tmp`, and found nothing. Then it changed the question from "who wrote this
file" to "who else ever had this data". It searched sessions for the file's
path and for the API that serves the same content, and found another agent
that, the day before, had `curl`ed that API into a temp file for an unrelated
comparison. The copy was still on disk. It restored the file, checked it byte
for byte, and the commit cites the event where the copy was made. Nobody told
it to use agentlog. 9 rounds, under 3 minutes.

## A feature deleted from the code, found by what it did

An agent told me it couldn't watch the macOS UI without taking over my screen.
I was sure an earlier agent had done exactly that. It didn't know what the
feature had been called, so it searched for the mechanism: which sessions ever
ran `screencapture`. That turned up a "Showcase window" in a Codex session from
173 days earlier. The code had been deleted from the repo months before, so
`git log -S` would only have found it if I'd remembered the name. 5 calls, 1.5
minutes. I was right, and its first answer was wrong.

## How a workaround was done last time

My `~/.npmrc` deliberately holds back npm packages published in the last five
days (`min-release-age`). Once I needed a Codex CLI release newer than that, and
I knew there was a way to run it once without changing anything. I told the
agent exactly that: "you need to use a newer version i forgot how agent did this
in the past but its because of npm version rules. dont change my version but
theres a way to run it". It searched old sessions for the npm error and found
one from a month earlier with the answer: run that one release through `npx`,
with the setting lifted for that single command. My installed version and
`~/.npmrc` stayed as they were. 4 calls, about a minute.

Another agent couldn't reply to its cloud lead. It searched three days of
sessions for a `SendMessage` call that had worked, copied its addressing, and
its replies got through two minutes later.

## My own words, as evidence

In projects where agents write almost all the text, my turns in the logs are
the only text whose author and time are both certain. About 63% of the 2,100
calls in my run log read them (`--user`).

- **Saying it once.** I wanted more of the thinking behind a short ideas doc,
  so an agent pulled my turns from past sessions, with the agents' replies
  kept for framing. That became a 38KB doc that later sessions read, and the
  project's memory now says to check earlier transcripts via agentlog before
  deciding an issue. I was burning out answering questions I'd already
  answered.
- **Reviewing an AI without its own notes.** For a midterm review I had a Codex
  agent read my Claude sessions: only what I gave Claude, not what Claude gave
  me, since 95% of that project's text was Claude's. The review lists my events
  by number, and it recovered homework images I'd pasted, including one from a
  queued message.
- **Who actually did the work.** For a lab, I asked for an honest split between
  my work and the agent's.

The limit, learned the hard way: the logs hold the chat side only. One audit
concluded I "never ran" three commands, because the agent ran them in the log.
My shell history showed I'd run them myself, got one wrong, then got it right.
Absence from the logs isn't absence from the machine.

## Across walls no harness sees past

- **An air-gapped VM.** For a security lab I ran Claude in a VM with networking
  cut off, then dragged the session file out to the host. The host agent put it
  where agentlog would read it and answered my question: did the model refuse
  anything during the lab? `--kind refusal` came back empty.
- **"GPT broke the dev dashboard."** The agent swept every Claude and Codex
  session by day and worktree, and grepped their tool calls for operational
  commands. It named the Codex session that restarted the server from the wrong
  checkout, the Claude subagent that restored it three minutes later, and an
  uncommitted one-line edit, each to the second. 17 calls, under 3 minutes.

## Measurements already taken

I was on battery and told an agent to stop running heavy compute. Within a
minute it pulled the timings from past sessions instead. The numbers were
right. The cause it read into them was wrong, and a real experiment later found
the actual one. Logs can stand in for a measurement already taken, not for an
experiment.

## Switching harnesses, and old sessions

When Claude runs out of limits I move to Codex, or the other way round, and
the new agent reads where the last one stopped. When a session is old, its
cache has expired, and another message or a compaction would cost too much, I
don't resume it: I start a fresh agent and tell it to look at what agents were
doing earlier and pick up where they left off. In some projects agentlog is
over a third of all token usage, because I lean on it that hard.

## When it was wrong

For months it silently missed every Codex session older than a week, because
Codex compresses old rollouts and agentlog only read plain ones. On my machine
it listed 81 Codex threads; I had 792. It reads them now, and a log it can't
read is reported, never dropped.
