package app

import (
	"reflect"
	"testing"
)

func TestFeedbackCalls(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		want       []string
	}{
		{"double quoted", `agentlog feedback add "grep was useful; refs were confusing"`, []string{"grep was useful; refs were confusing"}},
		{"bare words", `agentlog feedback add grep was useful`, []string{"grep was useful"}},
		{"piped output", `agentlog feedback add "It worked." 2>&1 | tail -1`, []string{"It worked."}},
		{"after cd", `cd /repo && agentlog feedback add 'it'"'"'s fine'`, []string{"it's fine"}},
		{"path to binary", `~/bin/agentlog feedback add x`, []string{"x"}},
		{"go run", `go run ./cmd/agentlog feedback add from source`, []string{"from source"}},
		{"env prefix", `AGENTLOG_JOBS=1 agentlog feedback add ok`, []string{"ok"}},
		{"codex bash -lc", `bash -lc 'agentlog feedback add "from codex"'`, []string{"from codex"}},
		{"heredoc", "agentlog feedback add - <<'EOF'\nline one\nline two\nEOF", []string{"line one\nline two"}},
		{"echo pipe", `echo "piped note" | agentlog feedback add -`, []string{"piped note"}},
		{"two in one line", `agentlog feedback add a; agentlog feedback add b`, []string{"a", "b"}},
		// The smoke test a Codex agent ran against its own build.
		{"test store and thread", `AGENTLOG_FEEDBACK_FILE=/tmp/f.jsonl CODEX_THREAD_ID=smoke /tmp/agentlog feedback add "smoke test" && cat /tmp/f.jsonl`, []string{"smoke test"}},
		{"ansi-c quoting", `agentlog feedback add $'line one\nline two\tend, it\'s fine'`, []string{"line one\nline two\tend, it's fine"}},
		{"escapes in double quotes", `agentlog feedback add "say \"hi\" to \$HOME, keep \n"`, []string{`say "hi" to $HOME, keep \n`}},
		{"line continuation", "agentlog feedback add \\\n  \"continued\"", []string{"continued"}},
		{"redirections are not words", `agentlog feedback add x > /tmp/out 2>/dev/null`, []string{"x"}},
		{"redirect against a word", `agentlog feedback add done>/dev/null`, []string{"done"}},
		{"backslash at the end", `agentlog feedback add trailing\`, []string{"trailing"}},
		{"background, all output away", `nohup agentlog feedback add bg >/dev/null 2>&1 &`, []string{"bg"}},
		{"&> redirect", `agentlog feedback add y &>/dev/null`, []string{"y"}},
		{"subshell", `(cd /repo && agentlog feedback add sub)`, []string{"sub"}},
		{"time", `time agentlog feedback add timed`, []string{"timed"}},
		{"or", `agentlog feedback add ok || echo failed`, []string{"ok"}},
		{"hash inside a word", `agentlog feedback add issue#12 is fixed # not this`, []string{"issue#12 is fixed"}},
		{"sh -c double quoted", `sh -c "agentlog feedback add 'inner'"`, []string{"inner"}},
		{"zsh -lc after cd", `zsh -lc "cd /x; agentlog feedback add 'deep'"`, []string{"deep"}},
		{"unterminated quote", `agentlog feedback add "cut off`, []string{"cut off"}},
		{"heredoc then another call", "agentlog feedback add - <<EOF\nfirst\nEOF\nagentlog feedback add second", []string{"first", "second"}},
		// <<- strips leading tabs from every body line, not only the delimiter.
		{"tab-stripped heredoc", "agentlog feedback add - <<-EOF\n\tone\n\ttwo\n\tEOF", []string{"one\ntwo"}},
		// Piped in from a heredoc, printf or echo: the text is what they print.
		{"cat heredoc pipe", "cat <<'EOF' | agentlog feedback add -\nfrom cat\nEOF", []string{"from cat"}},
		{"printf pipe", `printf '%s\n' "printf note" | agentlog feedback add -`, []string{"printf note"}},
		{"printf format only", `printf 'two\nlines' | agentlog feedback add -`, []string{"two\nlines"}},
		{"echo -n pipe", `echo -n "no newline" | agentlog feedback add -`, []string{"no newline"}},
		// Text agentlog cannot see: the command line stands in for it.
		{"file redirected in", `agentlog feedback add - < note.txt`, []string{"agentlog feedback add - < note.txt"}},
		{"file piped in", `cat note.txt | agentlog feedback add -`, []string{"cat note.txt | agentlog feedback add -"}},

		{"echoed", `echo agentlog feedback add x`, nil},
		{"grepped", `grep -rn "agentlog feedback add" .`, nil},
		{"heredoc data", "cat > notes.md <<'EOF'\nagentlog feedback add \"not me\"\nEOF", nil},
		{"python heredoc", "python3 - <<'EOF'\nprint('agentlog feedback add x')\nEOF", nil},
		{"list", `agentlog feedback`, nil},
		{"comment", `ls # agentlog feedback add x`, nil},
		{"commented after a call", `agentlog feedback add "a"; # agentlog feedback add b`, []string{"a"}},
		{"echoed by bash -c", `bash -c 'echo agentlog feedback add x'`, nil},
		{"help first", `agentlog --help feedback add x`, nil},
		{"bash with no script", `bash -x script.sh agentlog`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := feedbackCalls(tc.line); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("feedbackCalls(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

func TestShellWord(t *testing.T) {
	for in, want := range map[string]string{
		"invoices":      "invoices",
		"/work/shop":    "/work/shop",
		"/work/my shop": "'/work/my shop'",
		"it's":          `'it'\''s'`,
		"":              "''",
		`fix\.rounding`: `'fix\.rounding'`,
	} {
		if got := shellWord(in); got != want {
			t.Errorf("shellWord(%q) = %s, want %s", in, got, want)
		}
	}
}
