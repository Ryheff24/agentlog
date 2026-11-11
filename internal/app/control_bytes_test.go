package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Ryheff24/agentlog/internal/catalog"
	"github.com/Ryheff24/agentlog/internal/claude"
	"github.com/Ryheff24/agentlog/internal/codex"
	"github.com/Ryheff24/agentlog/internal/copilot"
)

// runSplit is run with stdout and stderr kept apart.
func (w *world) runSplit(cwd string, args ...string) (string, string) {
	w.t.Helper()
	cat := &catalog.Catalog{
		Providers: []catalog.Provider{claude.New(), codex.New(), copilot.New()},
		CWD:       cwd,
		Now:       w.now,
	}
	var out, errOut bytes.Buffer
	Run(Env{Catalog: cat, Stdout: &out, Stderr: &errOut, Ctx: context.Background()}, args)
	return out.String(), errOut.String()
}

// rawControl reports where s holds a byte an agent should never be handed
// raw: a C0 control other than newline and tab, DEL, a C1 control, or a byte
// that is not UTF-8. It returns -1 when there is none.
func rawControl(s string) int {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 || r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r <= 0x9f {
			return i
		}
		i += size
	}
	return -1
}

// jsonControl reports the first string in a JSON document that decodes to a
// control character, so an escape such as \u0001 counts as much as the byte.
func jsonControl(t *testing.T, out string) string {
	t.Helper()
	var v any
	dec := json.NewDecoder(strings.NewReader(out))
	for dec.More() {
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		if s := walkControl(v); s != "" {
			return s
		}
	}
	return ""
}

func walkControl(v any) string {
	switch t := v.(type) {
	case string:
		if rawControl(t) >= 0 {
			return t
		}
	case map[string]any:
		for k, x := range t {
			if rawControl(k) >= 0 {
				return k
			}
			if s := walkControl(x); s != "" {
				return s
			}
		}
	case []any:
		for _, x := range t {
			if s := walkControl(x); s != "" {
				return s
			}
		}
	}
	return ""
}

// Every view, in text and JSON, over a session full of control characters:
// none reaches the caller, as a byte or as a JSON escape.
func TestNoViewPrintsControlBytes(t *testing.T) {
	w := newWorld(t)
	id, _ := w.binaryWorld()
	ref := id[:8]
	views := [][]string{
		{}, {"--help"}, {"--list"}, {"bytes"}, {"bytes", "--list"}, {"all", "--list"},
		{ref}, {ref, "--full"}, {ref, "--brief"}, {ref, "--tail", "3"}, {ref, "--head", "3"},
		{ref + "#1-"}, {ref + "#1-", "--full"}, {ref + "#1-", "--max", "40"}, {ref + "#1-", "--brief"},
		{ref + "#2"}, {ref + "#3"}, {ref + "#4"}, {ref + "#3", "--full"}, {ref + "#2", "--max", "30"},
		{ref, "--kind", "tool"}, {ref, "--kind", "user,assistant,plan,edit"}, {ref, "--user"},
		{ref, "--grep", "tools-used"}, {ref, "--grep", "x1b|ELF|torn"}, {"all", "--grep", "tools-used"},
		{"bytes", "--grep", "firmware"}, {ref, "--grep", "ELF", "--count"}, {"firmware", "--content"},
		{ref, "--edits"}, {ref, "--files"}, {ref, "--tools"}, {ref, "--usage"}, {ref, "--tree"},
		{ref, "--subagents"}, {ref, "--reasoning"}, {"--projects"}, {"doctor"}, {"doctor", "claude", "all"},
		{"/work/bytes/term.go"}, {"term.go"}, {ref, "--images"}, {"feedback", "list"},
	}
	if out, code := w.run("/work/bytes", "feedback", "add", "the \x1b[1mgrep\x1b[0m view\x07 helped"); code != 0 {
		t.Fatalf("feedback add: %d %s", code, out)
	}
	if out, _ := w.run("/work/bytes", "feedback", "list", "--json"); !strings.Contains(out, "the ␛[1mgrep␛[0m view␇ helped") {
		t.Errorf("feedback list --json does not name the control characters:\n%s", out)
	}
	for _, args := range views {
		for _, asJSON := range []bool{false, true} {
			a := append([]string{}, args...)
			if asJSON {
				a = append(a, "--json")
			}
			out, errOut := w.runSplit("/work/bytes", a...)
			for _, o := range []string{out, errOut} {
				if i := rawControl(o); i >= 0 {
					t.Errorf("agentlog %s: raw control byte %q at %d in\n%s", strings.Join(a, " "), o[i], i, o)
				}
			}
			if trimmed := strings.TrimSpace(out); asJSON && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
				if s := jsonControl(t, out); s != "" {
					t.Errorf("agentlog %s: a JSON string decodes to a control character: %q", strings.Join(a, " "), s)
				}
			}
		}
	}
}

// The binary result is one line that says what it was and how to get it; the
// command it names prints it exactly as logged.
func TestBinaryResultIsAStandIn(t *testing.T) {
	w := newWorld(t)
	id, binary := w.binaryWorld()
	ref := id[:8]
	out, _ := w.run("/work/bytes", ref+"#2")
	want := "[binary, not shown: " + itoa(len(binary)) + " bytes, 202 control and 81 U+FFFD characters; agentlog " + ref + "#2 --bytes prints it as logged]"
	if !strings.Contains(out, want) {
		t.Fatalf("no stand-in %q in\n%s", want, out)
	}
	if strings.Contains(out, "ELF") || strings.Contains(out, "end of window") {
		t.Errorf("binary text printed beside its stand-in:\n%s", out)
	}
	if !strings.Contains(out, "python3 read.py --sep $'␁'") {
		t.Errorf("the call's input lost its readable form:\n%s", out)
	}

	exact, _ := w.run("/work/bytes", ref+"#2", "--bytes")
	if !strings.Contains(exact, "  "+strings.ReplaceAll(binary, "\n", "\n  ")) {
		t.Errorf("--bytes did not print the result as logged:\n%q", exact)
	}

	// Past the clip, the command also asks for --full.
	long, _ := w.run("/work/bytes", ref+"#2", "--max", "100")
	if !strings.Contains(long, "agentlog "+ref+"#2 --bytes --full prints it") {
		t.Errorf("a clipped binary result does not name --full:\n%s", long)
	}
}

// Text with a few control characters is printed with each one named, and
// everything else in it exactly as before.
func TestSparseControlsAreNamed(t *testing.T) {
	w := newWorld(t)
	id, _ := w.binaryWorld()
	ref := id[:8]
	out, _ := w.run("/work/bytes", ref+"#3")
	want := "  ␛[32mok␛[0m  3 passed\n  warning␇ 日本語 é\n  10%␍20%\n"
	if !strings.Contains(out, want) {
		t.Errorf("want %q in\n%q", want, out)
	}
	prompt, _ := w.run("/work/bytes", ref+"#1")
	if !strings.Contains(prompt, "find tools-used in the firmware␇ dump\n  please") {
		t.Errorf("prompt:\n%q", prompt)
	}
	diff, _ := w.run("/work/bytes", ref, "--edits")
	if !strings.Contains(diff, "esc := \"␛[0m\"") {
		t.Errorf("diff:\n%s", diff)
	}
	plan, _ := w.run("/work/bytes", ref+"#6")
	if !strings.Contains(plan, "strip the ␛ codes") {
		t.Errorf("plan:\n%s", plan)
	}
	title, _ := w.run("/work/bytes", "bytes", "--list")
	if !strings.Contains(title, "Read the ␛[1mfirmware␛[0m image") {
		t.Errorf("title:\n%s", title)
	}
	last, _ := w.run("/work/bytes", ref)
	if !strings.Contains(last, "Found it␈ at offset 0x40.") {
		t.Errorf("last message:\n%s", last)
	}
}

// Long hex and base64 runs are commit hashes, checksums and fixtures as often
// as anything else; they are printed as logged.
func TestHexAndBase64ArePrinted(t *testing.T) {
	w := newWorld(t)
	id, _ := w.binaryWorld()
	out, _ := w.run("/work/bytes", id[:8]+"#4")
	for _, want := range []string{strings.Repeat("0123456789abcdef", 4) + "  image", strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo", 9) + "=="} {
		if !strings.Contains(out, want) {
			t.Errorf("%.20s… not printed as logged:\n%s", want, out)
		}
	}
}

// A clip never leaves half a character behind, whatever byte it falls on.
func TestClipNeverSplitsACharacter(t *testing.T) {
	w := newWorld(t)
	id, _ := w.binaryWorld()
	for max := 8; max <= 60; max++ {
		out, _ := w.run("/work/bytes", id[:8]+"#3", "--max", itoa(max))
		if strings.Contains(out, "�") || rawControl(out) >= 0 {
			t.Errorf("--max %d: %q", max, out)
		}
	}
}

func TestBinaryInJSON(t *testing.T) {
	w := newWorld(t)
	id, binary := w.binaryWorld()
	ref := id[:8]
	for _, full := range []bool{false, true} {
		args := []string{ref + "#2", "--json"}
		if full {
			args = append(args, "--full")
		}
		out, _ := w.run("/work/bytes", args...)
		var r struct {
			Events []struct {
				Output, Input string
			}
			Truncated []struct {
				What  string
				Count int
				Bytes int
				Get   string
			}
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.HasPrefix(r.Events[0].Output, "[binary, not shown: ") || !strings.Contains(r.Events[0].Output, "agentlog "+ref+"#2 --bytes --json prints it") {
			t.Errorf("output %q", r.Events[0].Output)
		}
		if r.Events[0].Input == "" || rawControl(r.Events[0].Input) >= 0 {
			t.Errorf("input %q", r.Events[0].Input)
		}
		if len(r.Truncated) == 0 || r.Truncated[0].What != "binary fields not shown" || r.Truncated[0].Count != 1 ||
			r.Truncated[0].Bytes != len(binary) || r.Truncated[0].Get != "agentlog "+ref+"#N --bytes --json" {
			t.Errorf("truncated %+v", r.Truncated)
		}
	}

	out, _ := w.run("/work/bytes", ref+"#1-", "--bytes", "--json", "--full")
	if i := rawControl(out); i >= 0 {
		t.Fatalf("--bytes --json printed a raw control byte at %d", i)
	}
	var r struct {
		Events []struct{ Output, Text string }
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range r.Events {
		if e.Output == binary {
			found = true
		}
	}
	if !found {
		t.Error("--bytes --json --full does not decode to the result as logged")
	}
}

// --bytes stays on the hints that continue a selection.
func TestBytesCarriesThroughHints(t *testing.T) {
	w := newWorld(t)
	id, _ := w.binaryWorld()
	out, _ := w.run("/work/bytes", id[:8], "--tail", "2", "--bytes")
	if !strings.Contains(out, "--tail 2 --bytes") && !strings.Contains(out, "--bytes --tail 2") {
		t.Errorf("no --bytes in the continuation:\n%s", out)
	}
}
