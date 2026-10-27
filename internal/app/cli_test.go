package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The command lines agents actually run, against the world in world_test.go.
// The shapes come from 288 agentlog calls found in the local logs: a word
// with a recency and --list, an id preview, an id with --kind and --tail, a
// #N ref, --edits --files, a file path. Each case says what a reader of the
// output needs to see, and what would mislead them.
func TestCommands(t *testing.T) {
	w := newWorld(t)
	cases := []struct {
		name  string
		cwd   string
		args  []string
		want  []string // must all appear
		not   []string // must not appear
		order []string // must appear in this order
		code  int
	}{
		// ── Finding a session ──
		{
			name:  "home lists running work before this directory's completed sessions",
			cwd:   "/work/blog",
			want:  []string{"recent sessions", "usage: agentlog", "last prompt: keep the decimals"},
			not:   []string{"1b0c0000"}, // nine days old
			order: []string{"7e1e0000", "d4af0000", "5a1e0000", "01a0dddd", "c0c0c0c0"},
		},
		{
			name: "a word lists every session it names, by title or by prompt",
			cwd:  "/work/blog",
			args: []string{"checkout", "--list"},
			want: []string{"5a1e0000", "01a0dddd"},
			not:  []string{"d4af0000", "c0c0c0c0"},
		},
		{
			name: "an old session is found by widening, and the widening is said",
			cwd:  "/work/shop",
			args: []string{"invoices", "--list"},
			want: []string{"1b0c0000", "ALL TIME was searched instead"},
		},
		{
			name: "a time clue stops the widening",
			cwd:  "/work/shop",
			args: []string{"invoices", "2d", "--list"},
			want: []string{"no session matches", "'all' to search all time"},
			not:  []string{"1b0c0000"},
		},
		{
			name: "dot is this directory",
			cwd:  "/work/blog",
			args: []string{".", "--list"},
			want: []string{"d4af0000"},
			not:  []string{"5a1e0000", "01a0dddd"},
		},
		{
			name: "a harness name keeps that harness",
			cwd:  "/work/shop",
			args: []string{"codex", "--list"},
			want: []string{"01a0dddd"},
			not:  []string{"5a1e0000", "c0c0c0c0"},
		},
		{
			name: "a day holds the sessions that ran on it",
			cwd:  "/work/shop",
			args: []string{"2026-09-16", "--list"},
			want: []string{"1b0c0000"},
			not:  []string{"5a1e0000"},
		},
		{
			name: "a tie is listed, not guessed",
			cwd:  "/work/shop",
			args: []string{"blog"},
			want: []string{"2 sessions match word=blog equally well", "d4af0000", "d7af0000"},
		},
		{
			name: "a running session wins what would otherwise tie",
			cwd:  "/work/blog",
			args: []string{"shop"},
			want: []string{`"Ship the rounding fix"`, "other matches:", "5a1e0000", "01a0dddd"},
			// Newer than the rest of /work/shop, but nothing was said in them.
			not: []string{"c1ea0000", "0be40000"},
		},
		{
			name: "home leaves out sessions with nothing said in them, and names them",
			cwd:  "/work/shop",
			want: []string{"left out 2 sessions with no prompt in them (opened and cleared, or a shell command alone): c1ea0000 0be40000"},
			not:  []string{"c1ea0000  claude", "0be40000  claude"},
		},
		{
			name: "a blank session asked for by id is opened, its unknown start said as such",
			cwd:  "/work/shop",
			args: []string{"0be4"},
			want: []string{"claude 0be40000-0000-4000-8000-00000000000f", "time      ? → "},
		},
		{
			// "What am I working on, and where did I leave off": one entry
			// per repository, a worktree counted as its repository, parallel
			// sessions shown, not only the newest, and blank ones set aside.
			name: "--projects groups sessions by repository, newest work first",
			cwd:  "/work/shop",
			args: []string{"--projects", "all"},
			want: []string{
				"/work/shop  last work 14:59  5 sessions, 1 subagent\n  also in .claude/worktrees/tax",
				"0a0b0000  claude  14:59  first prompt: finish the rename in the docs  RUNNING  +1 subagent (1 running)",
				"      left off  The package is renamed; the docs still say pkg/client.",
				"  earlier   3c3c0000 1b0c0000",
				"left out 2 sessions with no prompt in them",
			},
			order: []string{"/work/shop  ", "/work/api  ", "/work/blog  ", "/work/site  ", "/work/ccr  "},
		},
		{
			name: "--projects --json",
			cwd:  "/work/shop",
			args: []string{"--projects", "--json"},
			want: []string{`"dir":"/work/shop"`, `"also_in":["/work/shop/.claude/worktrees/tax"]`, `"subagents":1,`},
			// The latest session is named, not dumped with its subagents.
			not: []string{`"parent_id"`},
		},
		{
			name: "a session is found in the directory it moved to",
			cwd:  "/work/shop",
			args: []string{"site", "--list"},
			want: []string{"3007ed00  claude  11:00  ", "/work/site  ended:done"},
		},
		{
			name: "a best match names the others it passed over",
			cwd:  "/work/blog",
			args: []string{"checkout", "shop"},
			want: []string{`"Fix checkout rounding"`, "other matches:", "7e1e0000", "01a0dddd"},
		},
		{
			name: "the clues are restated when nothing matches",
			cwd:  "/work/shop",
			args: []string{"codex", "zebra"},
			want: []string{"no session matches harness=codex word=zebra", "codex    2 sessions", "\n  agentlog codex all --grep zebra\n"},
			not:  []string{"claude   0 sessions"}, // it was not searched
		},
		{
			// Agents use a word clue to look for what was said.
			name: "a word that matches nothing points at a content search",
			cwd:  "/work/shop",
			args: []string{"invoices", "2d", "."},
			want: []string{"not what was said in it", "agentlog /work/shop 48h --grep invoices"},
		},
		{
			name: "a missing file is restated with the other clues",
			cwd:  "/work/shop",
			args: []string{"src/gone.ts", "zebra"},
			want: []string{"no session matches path=/work/shop/src/gone.ts word=zebra"},
		},
		{
			name: "the content search keeps a day",
			cwd:  "/work/shop",
			args: []string{"zebra", "sep16", "--json"},
			want: []string{`"agentlog 2026-09-16 --grep zebra"`},
		},
		{
			name: "nothing matches",
			cwd:  "/work/shop",
			args: []string{"zebra"},
			want: []string{"no session matches", "all time was searched; try fewer clues"},
			not:  []string{"'all' to search"}, // it already did
		},
		{
			name: "a file lookup finds the sessions that changed it",
			cwd:  "/work/shop",
			args: []string{"src/checkout.ts"},
			want: []string{"5a1e0000"},
			not:  []string{"d4af0000"},
		},
		{
			name: "a file lookup with --list lists only those sessions",
			cwd:  "/work/shop",
			args: []string{"src/checkout.ts", "--list"},
			want: []string{"5a1e0000"},
			not:  []string{"d4af0000", "c0c0c0c0"},
		},

		{
			// The same file in a worktree of the repository is the same file,
			// and the answer says which path the session actually changed.
			name:  "a file lookup finds the change made in a worktree, and says where",
			cwd:   "/work/shop",
			args:  []string{"src/checkout.ts"},
			want:  []string{"3c3c0000       1 change\n", "as .claude/worktrees/tax/src/checkout.ts", "a0b1c2d3e4f5a6b7c 2 mentions", ": 3 sessions\n"},
			order: []string{"5a1e0000", "3c3c0000", "a0b1c2d3e4f5a6b7c"}, // changes, newest first, then mentions
		},
		{
			// It used to suggest `agentlog REF#N --edits`, where --edits does
			// nothing.
			name: "a file lookup's next step opens the change",
			cwd:  "/work/shop",
			args: []string{"src/checkout.test.ts", "--limit", "1"},
			want: []string{"1 change, 1 mention", ": 2 sessions, 1 shown (--limit N for more)", "next: agentlog 01a0dddd-0000#5 shows that change in full; agentlog 01a0dddd-0000 --edits shows all of them"},
			not:  []string{"5a1e0000"},
		},
		{
			name: "a file only mentioned points at the mentions",
			cwd:  "/work/shop",
			args: []string{"claude", "src/checkout.test.ts"},
			want: []string{"5a1e0000       1 mention\n", `next: agentlog 5a1e0000 --grep 'checkout\.test\.ts' shows where it was mentioned`},
		},
		{
			name: "a session the API gave up on",
			cwd:  "/work/shop",
			args: []string{"3c3c"},
			want: []string{"state     stopped on an error", "error API Error: 529"},
		},
		{
			// The rename's log was last written by the pointer to where it
			// went on, well after its last step: the step's time is when
			// the work stopped.
			name: "--list says how each session ended and when its work stopped",
			cwd:  "/work/api",
			args: []string{".", "all", "--list"},
			want: []string{"0a0a0000  claude  10:24 (log touched 12:00)", "ended:continued", "ended:mid-tool", "0a0b0000  claude  14:59  5.1KB  /work/api  +1 subagent  RUNNING"},
		},
		{
			// The drafts session's instructions name the style guide; no event does.
			name: "a file named only outside the events",
			cwd:  "/work/blog",
			args: []string{"./STYLE.md"},
			want: []string{"for /work/blog/STYLE.md: 1 session named it but none touched it"},
			not:  []string{"next:"},
		},

		// ── A session's preview ──
		{
			name: "the preview says what happened and where to look next",
			cwd:  "/work/blog",
			args: []string{"5a1e"},
			want: []string{
				`claude 5a1e0000-0000-4000-8000-000000000001 "Fix checkout rounding"`,
				"idle after a reply",
				"Bash ×1 (1 failed)",
				"a0b1c2d3e4f5a6b7c  reviewer",
				"src/checkout.ts                               +1 -1",
				"2 events on 2 branches hidden",
				"team-lead → me",
				"last assistant message #15",
			},
			// the rewound prompt is not the session's history
			not: []string{"try a different approach with floats"},
		},
		{
			// A Claude compaction writes a boundary and then the summary it
			// handed the model; that is one compaction, not two.
			name: "one compaction is counted once",
			cwd:  "/work/shop",
			args: []string{"5a1e"},
			want: []string{"context compacted at #8 ("},
		},
		{
			name: "a subagent opens by its id",
			cwd:  "/work/shop",
			args: []string{"a0b1c2d3e4f5a6b7c"},
			want: []string{"reviewer", "Check the rounding change in src/checkout.ts.", "The change is right"},
		},
		{
			// Code mode logs a shell call as a script; the
			// session ended on a usage limit, not mid-turn.
			name: "codex code mode and a usage-limit ending",
			cwd:  "/work/shop",
			args: []string{"01a0dddd"},
			want: []string{"stopped on a usage/rate limit", "shell npm test -- checkout", "update src/checkout.test.ts (+1 -1)"},
			not:  []string{"exec", "mid-turn"},
		},
		{
			// A session being written now, waiting on a tool call. Its only
			// edit failed, so the file it names was not changed.
			name: "a running session with a plan, a commit and a failed edit",
			cwd:  "/work/shop",
			args: []string{"7e1e0000"},
			want: []string{
				"state     RUNNING",
				"commits   4f3a2b1 (#4) on main",
				"plan (latest):", "[x] Run the tests", "[>] Commit the fix", "[ ] Open the PR",
				"3 tool · 1 edit (1 failed)",
				"files changed (0; 1 only attempted", "not changed: 1 failed edit",
				"1 record of unrecognised type (session-weather ×1)",
				"gh pr checks 42 --watch → NO RESULT",
			},
			// The same record, said once.
			not: []string{"3 tool (1 failed)", "this build cannot read"},
		},
		{
			// MCP arguments have no key that names the call, so the
			// one-line view shows the first few, sorted.
			name: "an MCP call's arguments",
			cwd:  "/work/shop",
			args: []string{"7e1e0000", "--tail", "2"},
			want: []string{"github.create_pull_request base=main head=fix-rounding owner=acme +2 more"},
		},
		{
			name: "--edits lists commits above the diffs, and says an edit was not applied",
			cwd:  "/work/shop",
			args: []string{"7e1e0000", "--edits"},
			want: []string{"1 commit:", "4f3a2b1", "FAILED (not applied)"},
		},
		{
			name: "a long session lists its first and last prompts",
			cwd:  "/work/blog",
			args: []string{"d7af0000"},
			want: []string{"prompts (8; first and last 5):", "shorter intro", "… 2 more (--kind user)", "one more pass on the title", "state     interrupted"},
			not:  []string{"cut the second paragraph"},
		},
		{
			name: "codex goal, plan and the limit nearest its ceiling",
			cwd:  "/work/shop",
			args: []string{"01a0dddd"},
			want: []string{"goal      the checkout tests compare numbers [active]", "[>] compare as numbers", "primary (5h) limit 62% used"},
		},
		{
			name: "codex --usage reads the rate limits and when they reset",
			cwd:  "/work/shop",
			args: []string{"01a0dddd", "--usage"},
			want: []string{
				"30,000 cached input",
				"primary (5h) 62% used, resets Fri Sep 25 17:00 (in 2h00m)",
				"secondary (7d) 18% used, resets Mon Sep 28 15:00",
			},
		},
		{
			name: "copilot",
			cwd:  "/work/shop",
			args: []string{"c0c0"},
			want: []string{`"Fix the tunnel reconnect"`, "Fri Sep 25 10:00", "bash go test ./tunnel/..."},
		},

		// ── Views on one session ──
		{
			name: "--kind user is the prompts on the shown branch",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--kind", "user"},
			want: []string{"fix the rounding bug", "now add a test", "keep the decimals"},
			not:  []string{"Reading the checkout code", "floats"},
		},
		{
			name: "a kind that does not exist",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--kind", "bogus"},
			want: []string{`unknown --kind "bogus"`},
			code: 1,
		},
		{
			name: "--tail",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--tail", "2"},
			want: []string{"#14 ", "#15 "},
			not:  []string{"#13 "},
		},
		{
			name: "--head",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--head", "2"},
			want: []string{"#1 "},
			not:  []string{"#4 "},
		},
		{
			name: "--grep inside a session says what it could not see",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--grep", "floats"},
			want: []string{"--branches"},
			not:  []string{"Switched the totals to floats"},
		},
		{
			name: "--branches shows rewound turns and marks them",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--grep", "floats", "--branches"},
			want: []string{"[abandoned] try a different approach with floats", "[abandoned] Switched the totals to floats"},
		},
		{
			name: "--edits shows the diff",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--edits"},
			want: []string{"-  return round(sub) * tax", "+  return round(sub * tax)"},
		},
		{
			name: "--edits --files lists files without diffs",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--edits", "--files"},
			want: []string{"src/checkout.ts"},
			not:  []string{"return round"},
		},
		{
			name: "a new file shows as added",
			cwd:  "/work/shop",
			args: []string{"d4af", "--edits"},
			want: []string{"posts/launch.md", "+# Launch"},
		},
		{
			name: "a codex patch shows as a diff",
			cwd:  "/work/shop",
			args: []string{"01a0dddd", "--edits"},
			want: []string{"+  expect(total(9, 1.2)).toBeCloseTo(10.8)"},
		},
		{
			name: "--tools",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--tools"},
			want: []string{"Agent", "Bash", "Edit", "Read"},
		},
		{
			name: "--usage",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--usage"},
			want: []string{"claude-opus-5-5"},
		},
		{
			name: "--subagents folds the subagent in",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--subagents", "--tail", "40"},
			want: []string{"Check the rounding change"},
		},

		// ── Refs ──
		{
			name: "#N is one event in full",
			cwd:  "/work/shop",
			args: []string{"5a1e#6"},
			want: []string{"npm test -- checkout", "expected 10.8 to equal 10.80"},
		},
		{
			name: "#N-M",
			cwd:  "/work/shop",
			args: []string{"5a1e#4-5"},
			want: []string{"#4 tool", "\n#5 ", "round(sub * tax)"},
			not:  []string{"\n#6 "},
		},
		{
			name: "#N- runs to the end",
			cwd:  "/work/shop",
			args: []string{"5a1e#14-"},
			want: []string{"\n#14 ", "\n#15 ", "Where are we on checkout?"},
			not:  []string{"\n#13 "},
		},
		{
			name: "a ref past the end",
			cwd:  "/work/shop",
			args: []string{"5a1e#99"},
			want: []string{"no event #99: claude 5a1e0000 has 15 events"},
		},
		{
			// --kind was ignored in a range.
			name: "--kind narrows a range",
			cwd:  "/work/shop",
			args: []string{"5a1e#1-15", "--kind", "user"},
			want: []string{"#1 user", "\n#12 user"},
			not:  []string{"\n#3 "},
		},
		{
			name: "--max clips a long output",
			cwd:  "/work/shop",
			args: []string{"5a1e#6", "--max", "20"},
			not:  []string{"equal 10.80"},
		},

		{
			name: "--path narrows --edits to matching files",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--edits", "--path", `test\.ts$`},
			not:  []string{"return round"},
		},
		{
			name: "a bad --path",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--edits", "--path", "("},
			want: []string{"--path:"},
			code: 1,
		},
		{
			name: "a bad --grep",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--grep", "("},
			want: []string{"--grep:"},
			code: 1,
		},

		// ── Across sessions ──
		{
			name: "all --grep finds the event and gives its ref",
			cwd:  "/work/blog",
			args: []string{"all", "--grep", "expected 10.8"},
			want: []string{"5a1e0000", "#6"},
		},
		{
			// A search for something rewound found nothing
			// and gave no hint.
			name: "all --grep says it skipped rewound turns",
			cwd:  "/work/blog",
			args: []string{"all", "--grep", "floats"},
			want: []string{"--branches"},
		},

		{
			// A match no event shows used to be put down to rewound turns.
			name: "a search says a match is in a rewound turn",
			cwd:  "/work/shop",
			args: []string{"all", "--grep", "floats"},
			want: []string{"1 more session matched only outside the events shown: 1 in rewound or edited turns (add --branches"},
		},
		{
			name: "a search says a match is in a kind it left out",
			cwd:  "/work/shop",
			args: []string{"all", "--grep", "Reading the checkout", "--kind", "tool"},
			want: []string{"1 in events --kind leaves out"},
			not:  []string{"rewound"},
		},
		{
			name: "a search says a match is in no event",
			cwd:  "/work/shop",
			args: []string{"all", "--grep", "house rules"},
			want: []string{"1 in text that is not an event"},
			not:  []string{"rewound"},
		},
		{
			name: "--branches finds what the notice pointed at",
			cwd:  "/work/shop",
			args: []string{"all", "--grep", "floats", "--branches"},
			want: []string{"[abandoned] try a different approach with floats"},
			not:  []string{"matched only outside"},
		},
		{
			// The text is in a rewound turn and of a kind the search left
			// out; --branches alone would not show it.
			name: "a search says a rewound match is also of a kind it left out",
			cwd:  "/work/shop",
			args: []string{"all", "--grep", "floats", "--kind", "tool"},
			want: []string{"1 in events --kind leaves out"},
			not:  []string{"rewound"},
		},
		{
			name: "a bad --kind in a search",
			cwd:  "/work/shop",
			args: []string{"all", "--grep", "floats", "--kind", "tools"},
			want: []string{`unknown --kind "tools"`},
			code: 1,
		},
		{
			name: "a bad --kind on a range",
			cwd:  "/work/shop",
			args: []string{"5a1e#1-5", "--kind", "prompt"},
			want: []string{`unknown --kind "prompt"`},
			code: 1,
		},
		{
			name: "a tie longer than --limit says how many more",
			cwd:  "/work/shop",
			args: []string{"blog", "--limit", "1"},
			want: []string{"2 sessions match word=blog equally well", "1 session more matching equally well (--limit N)"},
		},
		{
			name: "a tie as JSON says what it left out",
			cwd:  "/work/shop",
			args: []string{"blog", "--limit", "1", "--json"},
			want: []string{`"truncated":[{"what":"further sessions matching equally well","count":1`},
		},
		{
			name: "--list --json says what it left out",
			cwd:  "/work/shop",
			args: []string{".", "--list", "--limit", "1", "--json"},
			want: []string{`{"what":"further matches","count":`},
		},
		{
			// The text view says "… N more"; the JSON said nothing.
			name: "the home screen as JSON says what it left out",
			cwd:  "/work/shop",
			args: []string{"--limit", "2", "--json"},
			want: []string{`"truncated":[{"what":"further sessions","count":`},
		},
		{
			// A view that has no census still says what it could not read.
			name: "a timeline ends with the unread records",
			cwd:  "/work/shop",
			args: []string{"7e1e0000", "--tail", "3"},
			want: []string{"1 record of 1 type this build cannot read"},
		},
		// ── Continuations, subagents, big sessions ──
		{
			// cbfe7aac: the earlier half of a conversation says where it went.
			name: "a session that was continued says where",
			cwd:  "/work/api",
			args: []string{"0a0a"},
			want: []string{
				"state     continued in 0a0b0000",
				"(last event 4h ago; log written 3h ago with nothing new)",
				"then      continued in 0a0b0000, which carries on from here (agentlog 0a0b0000)",
				"commits   a1a1a1a (#19) · b2b2b2b (#20) · c3c3c3c (#21) · d4d4d4d (#22) · +1 more on 2 branches; pushed (#21), with no push recorded after the last commit",
				"Edit ×13 · Bash ×5 · Glob ×1 · Grep ×1 · Read ×1 · +2 more tools",
				"files changed (13; most recent first):",
				"… 1 more (--edits --files)\n", // it has no subagents to include
			},
			not: []string{"cmd/api/main.go"}, // the oldest of the thirteen
		},
		{
			// cbfe7aac: the later half opens on the compaction that carried
			// it over; that compaction is the other session's, so only the
			// later one is this session's.
			name: "a continuation names what it continues, and its own compactions",
			cwd:  "/work/api",
			args: []string{"0a0b"},
			want: []string{
				"from      0a0a0000, compacted into this session; its earlier turns are there (agentlog 0a0a0000)",
				"note      context compacted at #11 (",
				"5 records of unrecognised type (agent-color ×1, hook-progress ×1, mcp-reconnect ×1, session-mode ×1, +1 more)",
			},
			not: []string{"compacted at #1,", "compacted at #1 "},
		},
		{
			// A background subagent still writing, with no .meta.json: its
			// task is read from its log, and what it changed is credited to it.
			name: "a running subagent with no meta file",
			cwd:  "/work/api",
			args: []string{"0a0b"},
			want: []string{
				"state     RUNNING",
				"subagents (1):\n  b5b6c7d8e9f0a1b2c    ",
				"  RUNNING\n      task #1: Replace pkg/client with pkg/apiclient in docs/.\n      1 edit\n      latest message #3 ",
				"files changed (1, including subagents;",
				"docs/client.md                                +1 -1  2 edits (1 failed) by b5b6c7d8e9f0a1b2c,main",
				"  agentlog b5b6c7d8e9f0a1b2c                 subagent\n",
			},
		},
		{
			// 94285df6: the session it continues is gone.
			name: "a continuation whose earlier log is gone",
			cwd:  "/work/api",
			args: []string{"0a0c"},
			want: []string{"from      an earlier session, compacted into this one; its log is not on disk, so the turns before #1 are gone",
				"state     stopped mid tool call (no result recorded)", "Bash go test ./... → NO RESULT",
				"commits   none; pushed retries (#6)"},
			not: []string{"context compacted at"},
		},
		{
			// A Codex team. Reasoning and inter-agent messages are
			// encrypted, the subagent's final answer is followed by a later
			// message, and the thread has two pages.
			name: "a codex multi-agent thread",
			cwd:  "/work/api",
			args: []string{"01a0eeee"},
			want: []string{
				"dir       /work/api (git rename @9c1d2e3 at start)",
				"(+1 earlier page file)\n",
				"01a0fffe-0000  Tesla /root/docs_audit  ",
				"final answer #3 ",
				"latest message #4 13:07: The stale link is in docs/index.md.",
				"docs/client.md                                +1 -1  1 edit by Tesla",
				"last 7 events (reasoning and 1 encrypted inter-agent message omitted):",
				"spent     9,700 tokens · primary (5h) limit 97% used at the time, reset since",
				"record [function_call_output with no call in this log] exit 0",
				"agentlog 01a0fffe-0000                 subagent Tesla /root/docs_audit",
			},
			not: []string{"One agent can read the docs"}, // reasoning
			// One patch changed both files at once; the tie breaks on the path.
			order: []string{"  docs/client.md    ", "  docs/index.md    "},
		},
		{
			name: "a codex subagent opens by id",
			cwd:  "/work/api",
			args: []string{"01a0fffe"},
			want: []string{"subagent  Tesla /root/docs_audit of parent 01a0eeee-0000", "files changed (2; most recent first):",
				"#2 13:04 update docs/index.md (+1 -1), update docs/client.md (+1 -1)", "#3 13:06 assistant [final] Updated docs/client.md"},
			not: []string{"prompts ("}, // its task came as an encrypted message, not a prompt
		},
		{
			name: "a codex plan's explanation",
			cwd:  "/work/api",
			args: []string{"01a0eeee#3"},
			want: []string{"  [>] audit the docs\n  [ ] fix what is stale\n  The docs are the slow part.\n"},
		},
		{
			// An agent that quotes a reading from a window that has since
			// turned over is quoting a number that no longer holds.
			name: "--usage says a limit's window has reset since it was read",
			cwd:  "/work/api",
			args: []string{"01a0eeee", "--usage"},
			want: []string{"limits    primary (5h) 97% used, window reset 30m ago — read 1h"},
		},
		{
			name: "--usage splits tokens by model after a /model switch",
			cwd:  "/work/api",
			args: []string{"0a0a", "--usage"},
			want: []string{"models    claude-opus-5-5 144,440\n          claude-sonnet-5-5 6,280\n"},
		},
		{
			// Plugin MCP tool names differ only at the end.
			name: "--tools keeps the end of a long tool name",
			cwd:  "/work/api",
			args: []string{"0a0a", "--tools"},
			want: []string{"…me-devtools-mcp_chrome-devtools.resize_page       1", "Edit                                              13              775B  #6-18"},
		},
		{
			name: "an MCP call's numeric arguments",
			cwd:  "/work/api",
			args: []string{"0a0a", "--tail", "2"},
			want: []string{"resize_page height=800 width=1280 → 7B"},
		},
		{
			name: "a session with no tool calls",
			cwd:  "/work/blog",
			args: []string{"d7af", "--tools"},
			want: []string{"no tool calls in this session"},
		},
		{
			name: "a subagent's changes are credited to it",
			cwd:  "/work/api",
			args: []string{"01a0eeee", "--edits", "--subagents"},
			want: []string{"── 01a0fffe-0000#2 13:04 update docs/client.md (+1 -1)\n   by Tesla\n"},
		},
		{
			// "+0 -0  1 edit" read as a change that happened to be empty.
			name: "--edits --files says a file was not changed",
			cwd:  "/work/shop",
			args: []string{"7e1e0000", "--edits", "--files"},
			want: []string{"CHANGELOG.md                                            not changed: 1 failed edit  by 7e1e0000"},
			not:  []string{"+0 -0"},
		},
		{
			name: "--edits --files with a failed and a landed edit of one file",
			cwd:  "/work/api",
			args: []string{"0a0b", "--edits", "--files", "--subagents"},
			want: []string{"docs/client.md                                          +1 -1  2 edits (1 failed)  by 0a0b0000,b5b6c7d8e9f0a1b2c"},
		},
		{
			// An event number means something only in its own session: a
			// combined table printed "Edit … #5-2".
			name: "--tools --subagents gives each session its own table",
			cwd:  "/work/api",
			args: []string{"0a0b", "--tools", "--subagents"},
			want: []string{"claude 0a0b0000 /work/api\n", "Edit        1       1", "\nclaude b5b6c7d8e9f0a1b2c /work/api\n", "1 call across 1 tool, 0 failed, 51B of output", "Edit       1               51B  #2-2"},
			not:  []string{"#5-2"},
		},
		{
			name: "--usage --subagents",
			cwd:  "/work/api",
			args: []string{"01a0eeee", "--usage", "--subagents"},
			// Codex writes usage; this subagent's log just has none.
			want: []string{"codex 01a0eeee-0000 /work/api\ntokens    9,700 total", "codex 01a0fffe-0000 Tesla /root/docs_audit\nno tokens recorded in this log\n"},
			not:  []string{"does not write usage"},
		},
		{
			name: "a teammate's message is not a call",
			cwd:  "/work/shop",
			args: []string{"5a1e#14"},
			want: []string{"message: team-lead → me\n  Where are we on checkout?\n"},
			not:  []string{"call:", "result:"},
		},
		{
			name: "a background agent's call names its session",
			cwd:  "/work/api",
			args: []string{"0a0b#4"},
			want: []string{"call: Agent general-purpose (session b5b6c7d8e9f0a1b2c)", "message:\n  Replace pkg/client", "result → 34B:\n  Async agent launched successfully."},
		},
		{
			name: "a plan in full",
			cwd:  "/work/shop",
			args: []string{"7e1e0000#2"},
			want: []string{"#2 plan · ", "  [x] Run the tests\n  [>] Commit the fix\n  [ ] Open the PR\n"},
		},
		{
			name: "a range with --branches marks the abandoned turns",
			cwd:  "/work/shop",
			args: []string{"5a1e#12-13", "--branches"},
			want: []string{"ABANDONED BRANCH (this turn was rewound or edited)"},
		},
		{
			name: "a range past the end stops at the last event",
			cwd:  "/work/shop",
			args: []string{"5a1e#14-40"},
			want: []string{"#15 assistant"},
			not:  []string{"(next:", "no event"},
		},
		{
			// `agentlog claude` opens the best Claude session and lists the
			// rest, five at most.
			name: "a harness clue lists at most five other matches",
			cwd:  "/work/shop",
			args: []string{"claude"},
			want: []string{"other matches:", "  … 3 more (--list)"},
		},
		{
			name: "doctor names what it cannot read, by version",
			cwd:  "/work/shop",
			args: []string{"doctor"},
			want: []string{
				"claude    2.1.281 ×8, 2.2.0 ×1, +1 more",
				"agent-color                        ×1      in 1 session    agentlog 0a0b0000#6",
				"session-weather                    ×1      in 1 session    agentlog 7e1e0000#5\n            forecast: sunny",
				"codex     0.130.0 ×2",
				"1 tool result whose call was not found, shown on their own",
			},
		},
		{
			// It used to report the harnesses left out as having no sessions.
			name: "doctor for one harness reports only that harness",
			cwd:  "/work/shop",
			args: []string{"doctor", "codex"},
			want: []string{"codex     0.130.0 ×2"},
			not:  []string{"claude", "copilot", "no sessions"},
		},
		{
			name: "doctor says what window was empty",
			cwd:  "/work/shop",
			args: []string{"doctor", "copilot", "1h"},
			want: []string{"copilot   no sessions since Sep 25 14:00 (add 'all' to look further back)"},
		},

		// ── The command line itself ──
		{
			name: "--help",
			cwd:  "/work/shop",
			args: []string{"--help"},
			want: []string{"usage: agentlog"},
		},
		{
			name: "an unknown flag",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--bogus"},
			want: []string{"--bogus"},
			code: 2,
		},
		{
			name: "a flag that needs a value, at the end",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--tail"},
			want: []string{"--tail needs a value"},
			code: 2,
		},
		{
			name: "a flag that needs a number",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--tail", "ten"},
			want: []string{`--tail wants a number, got "ten"`},
			code: 2,
		},
		{
			name: "--flag=value",
			cwd:  "/work/shop",
			args: []string{"5a1e#6", "--max=20"},
			not:  []string{"equal 10.80"},
		},
		{
			name: "after --, everything is a clue",
			cwd:  "/work/shop",
			args: []string{"--", "--tail"},
			want: []string{"no session matches word=--tail"},
		},
		{
			// Agents raise --jobs to go faster; the note is for them.
			name: "--jobs says what it took",
			cwd:  "/work/shop",
			args: []string{"5a1e", "--jobs", "2"},
			want: []string{"reading with 2 workers", "only raise --jobs when the person asks"},
		},
		{
			name: "doctor reports each harness",
			cwd:  "/work/shop",
			args: []string{"doctor"},
			want: []string{"claude", "codex", "copilot"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code := w.run(c.cwd, c.args...)
			if code != c.code {
				t.Errorf("exit %d, want %d", code, c.code)
			}
			for _, s := range c.want {
				if !strings.Contains(out, s) {
					t.Errorf("missing %q", s)
				}
			}
			for _, s := range c.not {
				if strings.Contains(out, s) {
					t.Errorf("has %q", s)
				}
			}
			at := 0
			for _, s := range c.order {
				i := strings.Index(out[at:], s)
				if i < 0 {
					t.Errorf("%q not after the previous entry of the order", s)
					break
				}
				at += i + len(s)
			}
			if t.Failed() {
				t.Logf("agentlog %s\n%s", strings.Join(c.args, " "), out)
			}
		})
	}
}

// The caller's own session is hidden from searches, since an agent looking
// for its earlier work should not find the session it is typing into; but
// asked for by id it opens.
func TestOwnSessionIsHiddenUnlessNamed(t *testing.T) {
	w := newWorld(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", checkoutID)

	out, _ := w.run("/work/shop", ".", "--list")
	if strings.Contains(out, "5a1e0000") || !strings.Contains(out, "your own session is hidden") {
		t.Errorf("own session listed, or its hiding not said:\n%s", out)
	}
	out, code := w.run("/work/shop", "5a1e0000")
	if code != 0 || !strings.Contains(out, "Fix checkout rounding") {
		t.Errorf("own session did not open by id (exit %d):\n%s", code, out)
	}
	out, _ = w.run("/work/shop", ".", "--list", "--json")
	if strings.Contains(out, checkoutID) || !strings.Contains(out, "your own session is hidden; pass its id to include it") {
		t.Errorf("own session listed, or its hiding not said, in JSON:\n%s", out)
	}
}

// --json is for scripts: it has to decode, and hold what the text view shows.
func TestJSON(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{
		{"5a1e", "--json"},
		{"5a1e#4", "--json"},
		{"5a1e", "--kind", "user", "--json"},
		{"checkout", "--list", "--json"},
		{"5a1e", "--edits", "--json"},
		{"zebra", "--json"},
		{"--json"},
		{"blog", "--json"},
		{"all", "--grep", "expected", "--json"},
		{"5a1e0000", "--tools", "--json"},
		{"01a0dddd", "--usage", "--json"},
		{"2026-09-16", "--list", "--json"},
		{"src/checkout.ts", "--json"},
		{"doctor", "--json"},
		{"0a0b", "--tools", "--subagents", "--json"},
		{"01a0eeee", "--usage", "--subagents", "--json"},
		{"0a0b", "--subagents", "--edits", "--json"},
		{"0a0b", "--subagents", "--kind", "edit", "--json"},
	} {
		out, code := w.run("/work/shop", args...)
		if code != 0 {
			t.Errorf("%v: exit %d\n%s", args, code, out)
			continue
		}
		var v any
		dec := json.NewDecoder(strings.NewReader(out))
		for dec.More() {
			if err := dec.Decode(&v); err != nil {
				t.Errorf("%v: not JSON: %v\n%s", args, err, out)
				break
			}
		}
	}

	out, _ := w.run("/work/shop", "5a1e#4", "--json")
	if !strings.Contains(out, `"Read"`) || !strings.Contains(out, "round(sub) * tax") {
		t.Errorf("#4 as JSON lacks the Read call and its output:\n%s", out)
	}

	var doc struct {
		Health  []health `json:"health"`
		Notices []string `json:"notices"`
	}
	out, _ = w.run("/work/shop", "doctor", "--json")
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Health) != 3 {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	if doc.Health[1].Provider != "codex" || doc.Health[1].Unpaired != 1 || len(doc.Health[0].Types) != 6 ||
		len(doc.Notices) != 1 || !strings.Contains(doc.Notices[0], fmt.Sprintf("; %.1f%% of records read by the worst harness", 100*doc.Health[0].Coverage())) {
		t.Errorf("doctor --json: %+v %q", doc.Health, doc.Notices)
	}

	// Counted across a session and its subagents, a first or last event
	// number would name an event in one of them, and not say which.
	out, _ = w.run("/work/api", "0a0b", "--tools", "--subagents", "--json")
	if strings.Contains(out, "first_event") || !strings.Contains(out, `{"tool":"Edit","calls":2,"failed":1,`) {
		t.Errorf("--tools --subagents --json:\n%s", out)
	}
}

// Feedback reaches the list whether it was saved to the store or, from inside
// a sandbox that refuses the store, left in the agent's own log.
func TestFeedbackFromTheStoreAndFromLogs(t *testing.T) {
	w := newWorld(t)
	if out, code := w.run("/work/shop", "feedback", "add", "the preview is too long"); code != 0 || !strings.Contains(out, "feedback saved") {
		t.Fatalf("add (exit %d):\n%s", code, out)
	}
	out, code := w.run("/work/shop", "feedback")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, s := range []string{"the preview is too long", "a word search should say when it matched titles only"} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q:\n%s", s, out)
		}
	}
}

// A type the person declared in types.json is known, so it is not drift: the
// release session's session-weather record is passed over, not unrecognised.
func TestDeclaredTypeIsNotDrift(t *testing.T) {
	w := newWorld(t)
	w.write(filepath.Join(w.root, "config", "types.json"), 0,
		`{"claude": [{"type": "session-weather", "role": "ignore", "note": "harness weather"}]}`)
	out, _ := w.run("/work/shop", "7e1e0000")
	if strings.Contains(out, "unrecognised") || !strings.Contains(out, "session-weather ×1") {
		t.Fatalf("a declared type was reported as drift:\n%s", out)
	}
	out, _ = w.run("/work/shop", "7e1e0000", "--tail", "3")
	if strings.Contains(out, "cannot read") {
		t.Fatalf("a declared type was reported as drift:\n%s", out)
	}
}

// A log agentlog may not read, such as one a sandbox or another user's
// permissions keep from it, is counted as unreadable, not skipped in silence.
func TestUnreadableLog(t *testing.T) {
	w := newWorld(t)
	events := filepath.Join(w.root, "copilot", "session-state", copilotID, "events.jsonl")
	if err := os.Chmod(events, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(events, 0o644) })
	if f, err := os.Open(events); err == nil {
		f.Close()
		t.Skip("this user can read a file with no permissions")
	}
	out, _ := w.run("/work/shop", "doctor", "copilot")
	if !strings.Contains(out, "copilot   1 log file could not be read at all") || strings.Contains(out, "% read") {
		t.Errorf("doctor:\n%s", out)
	}
	if out, code := w.run("/work/shop", "c0c0"); code != 1 || !strings.Contains(out, "permission denied") {
		t.Errorf("open (exit %d):\n%s", code, out)
	}
	out, _ = w.run("/work/shop", "all", "--grep", "tunnel")
	if !strings.Contains(out, "searched 17 sessions of 18 sessions") {
		t.Errorf("search:\n%s", out)
	}
}

// A machine with no logs yet, as on a first run: every view says so rather
// than failing.
func TestNoLogsAtAll(t *testing.T) {
	root := t.TempDir()
	for k, v := range map[string]string{"CLAUDE_CONFIG_DIR": "claude", "CODEX_HOME": "codex", "COPILOT_HOME": "copilot", "AGENTLOG_FEEDBACK_FILE": "feedback.jsonl", "AGENTLOG_CONFIG": "config"} {
		t.Setenv(k, filepath.Join(root, v))
	}
	for _, v := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "COPILOT_SESSION_ID"} {
		t.Setenv(v, "")
	}
	w := &world{t: t, root: root, now: time.Date(2026, 9, 25, 15, 0, 0, 0, time.Local)}
	for _, c := range []struct {
		args []string
		want []string
	}{
		{nil, []string{"claude   ", "not found", "no sessions in all time", "usage: agentlog"}},
		{[]string{"doctor"}, []string{"claude    not installed, or no logs where this build looks", "copilot   not installed"}},
		{[]string{"doctor", "--json"}, []string{`"present":false`, "100.0% of records read by the worst harness"}},
		{[]string{"feedback"}, []string{"0 notes: 0 from agents' logs (searched 0 sessions), 0 from "}},
		{[]string{"src/app.ts"}, []string{"no session matches path=/work/src/app.ts"}},
	} {
		out, code := w.run("/work", c.args...)
		for _, s := range c.want {
			if code != 0 || !strings.Contains(out, s) {
				t.Errorf("agentlog %s: exit %d, missing %q:\n%s", strings.Join(c.args, " "), code, s, out)
			}
		}
	}
}

// Agents copy the log path from a preview's "log" line and pass it back.
func TestOpenByLogPath(t *testing.T) {
	w := newWorld(t)
	path := filepath.Join(w.claudeProject("/work/blog"), launchID+".jsonl")
	out, code := w.run("/work/shop", path)
	if code != 0 || !strings.Contains(out, `claude d4af0000-0000-4000-8000-000000000003 "Draft the launch post"`) {
		t.Errorf("exit %d:\n%s", code, out)
	}
	out, _ = w.run("/work/shop", path, "--kind", "user", "--json")
	if !strings.Contains(out, `"query":"file=`+path) || !strings.Contains(out, "draft the launch post") {
		t.Errorf("--json:\n%s", out)
	}
}
