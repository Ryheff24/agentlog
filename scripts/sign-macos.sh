#!/bin/sh
# Signs and notarizes the macOS binary of a draft release on this Mac, then
# publishes the release. Run it after the release workflow has built the
# draft for a tag:
#
#   scripts/sign-macos.sh v0.1.0
#
# The signing key never leaves this Mac's keychain, so a compromised GitHub
# account can't produce a binary signed as the maintainer, and install.sh
# refuses any macOS binary without that signature. One-time setup is in
# CLAUDE.md under "Releases":
#
#   - a "Developer ID Application" certificate in the login keychain
#   - a notarytool keychain profile, named by AGENTLOG_NOTARY_PROFILE
#     (default: agentlog)
#
# To publish a draft without signing it: gh release edit vX.Y.Z --draft=false
set -eu
cd "$(dirname "$0")/.."
tag=${1:?usage: scripts/sign-macos.sh vX.Y.Z}
repo=Ryheff24/agentlog
profile=${AGENTLOG_NOTARY_PROFILE:-agentlog}
asset=agentlog-darwin-universal.tar.gz

fail() {
	echo "sign-macos: $*" >&2
	exit 1
}

identity=$(security find-identity -v -p codesigning | awk -F'"' '/"Developer ID Application: / { print $2; exit }')
[ -n "$identity" ] || fail "no Developer ID Application certificate in the keychain"
team=$(printf '%s' "$identity" | sed -n 's/.*(\([A-Z0-9]*\))$/\1/p')
grep -q "^team_id=\"$team\"$" install.sh || fail "install.sh does not pin team_id=\"$team\"; set it and push first, or every macOS install will refuse this binary"
[ "$(gh release view "$tag" -R "$repo" --json isDraft -q .isDraft)" = true ] || fail "$tag is not a draft release"

work=$(mktemp -d "${TMPDIR:-/tmp}/agentlog-sign.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir "$work/x" "$work/out"
gh release download "$tag" -R "$repo" -p "$asset" -p checksums.txt -D "$work"

# Only sign what the release workflow built from the tag.
gh attestation verify "$work/$asset" -R "$repo" \
	--signer-workflow "github.com/$repo/.github/workflows/release.yml" > /dev/null ||
	fail "$asset has no provenance from the release workflow; not signing it"

tar -xzf "$work/$asset" -C "$work/x" agentlog
codesign --force --options runtime --timestamp --sign "$identity" "$work/x/agentlog"
codesign --verify --strict --verbose=2 "$work/x/agentlog"

# A bare binary can't carry a stapled ticket; Gatekeeper looks the
# notarization up online the first time it runs.
ditto -c -k "$work/x/agentlog" "$work/notarize.zip"
result=$(xcrun notarytool submit "$work/notarize.zip" --keychain-profile "$profile" --wait --output-format json)
printf '%s' "$result" | grep -q '"status":"Accepted"' || fail "notarization was not accepted: $result"

tar -C "$work/x" -czf "$work/out/$asset" agentlog
sum=$(shasum -a 256 "$work/out/$asset" | cut -d' ' -f1)
awk -v f="$asset" -v s="$sum" '$2 == f { $1 = s } { print $1 "  " $2 }' "$work/checksums.txt" > "$work/out/checksums.txt"
gh release upload "$tag" -R "$repo" --clobber "$work/out/$asset" "$work/out/checksums.txt"
gh release edit "$tag" -R "$repo" --draft=false
echo "published $tag; macOS binary signed by $identity and notarized"
