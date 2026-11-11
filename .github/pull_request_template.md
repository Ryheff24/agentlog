## What changed and why

<!-- The problem or want, and the ISSUES.md or IDEAS.md entry it closes. -->

## End-to-end test

<!-- The real question you asked the built binary, what came back, how you
checked it against the raw logs, and what it cost (calls, bytes, seconds). -->

## Checks

- [ ] `gofmt -l .` is empty, and `go vet -composites=false ./...` and `go test ./...` pass
- [ ] `scripts/coverage.sh --check` passes (or say why it couldn't run)
- [ ] Tests cover the new behaviour, including edge cases from real logs
- [ ] `docs/reference.md` updated in the same change; `DESIGN.md` if a decision changed
- [ ] A fix removes its ISSUES.md entry
- [ ] If `agentlog` or `agentlog --help` prints something different, the pinned screens are updated (`go test ./internal/app -run TestScreens -update`) and the reason is above
- [ ] No raw logs, real session ids or private paths in the diff
