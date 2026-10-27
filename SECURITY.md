# Security

Please report a vulnerability privately through GitHub: the repository's
**Security** tab, then **Report a vulnerability**. Don't open a public issue
for it.

What counts: anything that lets agentlog write outside its own files, reach
the network, run another program, or be steered by someone else on the same
machine; a release archive or install path that can be swapped without
detection; and output that can act on a person's terminal.

What doesn't: agentlog printing secrets or untrusted text that are in the
logs it was asked to read. That's what it does, and the README says so.

## How releases are made

- A `v*` tag runs `.github/workflows/release.yml`, which tests, builds every
  archive, attests where each one came from, and creates a draft release.
- The maintainer signs and notarizes the macOS binary on their own Mac with
  `scripts/sign-macos.sh`, which only signs an archive whose attestation
  shows it came from that workflow, then publishes the release.
- `install.sh` checks the archive against `checksums.txt` and, on macOS,
  refuses a binary not signed by the maintainer's Apple team.
- Actions are pinned to commit hashes, the workflows' tokens are read-only
  except where a release is created, and nothing is cached between runs that
  feed a release.
