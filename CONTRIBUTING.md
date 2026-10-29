# Contributing

agentlog's users are agents, and most of its changes are made by agents too.
A change is worth making when it makes an agent's work cheaper: fewer calls,
fewer bytes into its context, less waiting, or no need for a one-off script.
Whoever judges whether a change works should use it the way an agent would.

[CLAUDE.md](CLAUDE.md) (the same text as [AGENTS.md](AGENTS.md)) is the full
working guide. This page is the short version for anyone opening an issue or
a pull request.

## Issues

- **Reproduce it first**, against real logs. Say what you ran, what came back,
  and how you know it's wrong. Check against the raw log, file times or git
  history, not against agentlog itself.
- **Run `agentlog doctor`** when output looks off. Harnesses change their log
  formats about weekly, and doctor shows what this build can't read yet.
- **Bugs come before features.** For a feature, say what you were trying to
  find out and what it cost you without it.
- **Never paste a raw session log.** Logs hold prompts, file contents, paths
  and sometimes secrets. Cut an excerpt down to the record shape that matters
  and replace the values.
- **Security problems go through [SECURITY.md](SECURITY.md)**, not an issue.

A reproduced problem is tracked in [ISSUES.md](ISSUES.md) until the commit
that fixes it. A want too big to build now goes in [IDEAS.md](IDEAS.md). A
problem deliberately left alone is in ISSUES.md under "Not acting on", with
the reason.

## Pull requests

Before opening one:

```sh
gofmt -l .
go vet -composites=false ./...
go test ./...
scripts/coverage.sh --check
```

CI runs the first three, plus govulncheck, on macOS and Linux. CI runners
have no harness logs, so the coverage baseline is checked locally. If you
don't have the logs to run it, say so in the pull request.

Then:

- **Test the behaviour.** Fixtures are shaped like real logs: the machine-wide
  world in `internal/app/world_test.go` and the redacted record shapes in
  `internal/shapes/testdata`. After adding tests, run
  `scripts/coverage.sh --update`.
- **Run an end-to-end test.** Build the binary, ask it a real question aimed at
  what you changed, and check the answer against the raw logs. In the pull
  request, say what you asked, what came back, how you checked it, and what it
  cost (calls, bytes, seconds).
- **Update the docs in the same change.** [docs/reference.md](docs/reference.md)
  describes every behaviour; [DESIGN.md](DESIGN.md) changes when a decision
  does. A fix removes its ISSUES.md entry.
- **One concern per commit.**

## What doesn't change

- No dependencies: pure Go, with no database, daemon or index between agentlog
  and the logs.
- It only reads. `cmd/agentlog/deps_test.go` fails if the binary links
  networking or `os/exec`; don't loosen it.
- Output is bounded. More detail comes from drilling down (`#N`, ranges,
  `--full`), never by default.
- A format change in a harness's logs makes agentlog degrade, never break or
  go silent.
- Don't weaken `install.sh`'s checks, `internal/private` (agentlog's own files
  are private and never written through links) or `internal/term` (control
  characters are escaped in a terminal).
- Copilot support is kept working but isn't being extended.

## License

Contributions are under the [Apache License 2.0](LICENSE), the same as the
rest of the project.
