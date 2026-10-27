package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/catalog"
	"github.com/Ryheff24/agentlog/internal/engine"
	"github.com/Ryheff24/agentlog/internal/private"
	"github.com/Ryheff24/agentlog/internal/render"
	"github.com/Ryheff24/agentlog/internal/scan"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Feedback lives in two places. An agent's note is read back from its own
// session log: the harness records the `agentlog feedback add` command line,
// message included, before it runs, and every sandbox lets agentlog read
// those logs. Nothing is written, so no sandbox can refuse the note and no
// agent is tempted to retry it outside one. A run that does not know it is
// an agent's still succeeds when the store refuses the write. The JSONL store in the user's
// config directory is for a note added from outside any agent session, and
// still holds notes saved before agents' notes moved to the logs.

// feedbackEntry is one note about using agentlog. JSONL keeps the store
// useful on its own while allowing concurrent writers to append safely.
type feedbackEntry struct {
	Outcome   string    `json:"outcome,omitempty"`
	Time      time.Time `json:"time"`
	Provider  string    `json:"provider,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	CWD       string    `json:"cwd,omitempty"`
	Message   string    `json:"message"`
	Ref       string    `json:"ref,omitempty"` // where in a session log the note was found
}

func feedback(env Env, args []string) error {
	if len(args) > 0 && args[0] == "add" {
		return addFeedback(env, args[1:])
	}
	jsonOutput := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && args[0] == "list" {
		args = args[1:]
		jsonOutput = len(args) == 1 && args[0] == "--json"
	}
	if len(args) != 0 && !(len(args) == 1 && args[0] == "--json") {
		return fmt.Errorf("usage: agentlog feedback [list [--json] | add MESSAGE]")
	}
	return listFeedback(env, jsonOutput)
}

func addFeedback(env Env, words []string) error {
	outcome := ""
	if len(words) > 0 && (words[0] == "--ok" || words[0] == "--fallback") {
		outcome = strings.TrimPrefix(words[0], "--")
		words = words[1:]
	}
	message := strings.TrimSpace(strings.Join(words, " "))
	if len(words) == 1 && words[0] == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read feedback: %w", err)
		}
		message = strings.TrimSpace(string(b))
	}
	if message == "" {
		return fmt.Errorf("usage: agentlog feedback add MESSAGE (or pipe text to `agentlog feedback add -`)")
	}

	provider, sessionID := currentAgent()
	if sessionID != "" {
		fmt.Fprintln(env.Stdout, "feedback noted: `agentlog feedback` reads it from this session's log, so nothing needs saving")
		return nil
	}
	cwd := ""
	if env.Catalog != nil {
		cwd = env.Catalog.CWD
	}
	entry := feedbackEntry{Outcome: outcome, Time: time.Now(), Provider: provider, SessionID: sessionID, CWD: cwd, Message: message}
	path, err := feedbackPath()
	if err != nil {
		return err
	}
	if err := appendFeedback(path, entry); err != nil {
		// A run that may not write is a sandboxed one, which is an agent's
		// whatever its environment says, so its log holds the note.
		if errors.Is(err, fs.ErrPermission) {
			fmt.Fprintf(env.Stdout, "feedback noted: %s is not writable here, and `agentlog feedback` reads the note from this session's log instead\n", path)
			return nil
		}
		return err
	}
	fmt.Fprintf(env.Stdout, "feedback saved: %s\n", path)
	return nil
}

func currentAgent() (provider, sessionID string) {
	for _, candidate := range []struct {
		provider string
		env      string
	}{
		{"codex", "CODEX_THREAD_ID"},
		{"claude", "CLAUDE_CODE_SESSION_ID"},
		{"copilot", "COPILOT_SESSION_ID"},
	} {
		if id := os.Getenv(candidate.env); id != "" {
			return candidate.provider, id
		}
	}
	return "", ""
}

func feedbackPath() (string, error) {
	if path := os.Getenv("AGENTLOG_FEEDBACK_FILE"); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(dir, "agentlog", "feedback.jsonl"), nil
}

func appendFeedback(path string, entry feedbackEntry) error {
	if err := private.Dir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create feedback directory: %w", err)
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode feedback: %w", err)
	}
	b = append(b, '\n')
	f, err := private.Open(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return fmt.Errorf("open feedback store: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		return fmt.Errorf("save feedback: %w", err)
	}
	return nil
}

func listFeedback(env Env, jsonOutput bool) error {
	w := env.Stdout
	path, err := feedbackPath()
	if err != nil {
		return err
	}
	stored, bad, err := readFeedback(path)
	if err != nil {
		return err
	}
	logged, searched, err := harvestFeedback(env)
	if err != nil {
		return err
	}
	entries := mergeFeedback(stored, logged)

	if jsonOutput {
		if entries == nil {
			entries = []feedbackEntry{}
		}
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(entries)
	}
	for _, entry := range entries {
		who := entry.Provider
		switch {
		case entry.Ref != "":
			who += " " + entry.Ref
		case entry.SessionID != "":
			who += " " + entry.SessionID
		}
		if strings.TrimSpace(who) == "" {
			who = "no agent session"
		}
		fmt.Fprintf(w, "%s · %s\n%s\n", entry.Time.Local().Format("2006-01-02 15:04"), strings.TrimSpace(who), entry.Message)
		if entry.CWD != "" {
			fmt.Fprintf(w, "cwd: %s\n", entry.CWD)
		}
		fmt.Fprintln(w)
	}
	fromLogs := 0
	for _, e := range entries {
		if e.Ref != "" {
			fromLogs++
		}
	}
	fmt.Fprintf(w, "%s: %d from agents' logs (%s), %d from %s\n",
		plural(len(entries), "note"), fromLogs, searched, len(entries)-fromLogs, path)
	if bad > 0 {
		fmt.Fprintf(w, "left out %s of %s that could not be read as a note\n", plural(bad, "line"), path)
	}
	return nil
}

// readFeedback reads the store. A line that is not a note, such as one cut
// short by a write that did not finish, is counted and passed over: it must
// not hide every other note.
func readFeedback(path string) (entries []feedbackEntry, bad int, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("open feedback store: %w", err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for s.Scan() {
		if len(bytes.TrimSpace(s.Bytes())) == 0 {
			continue
		}
		var entry feedbackEntry
		if json.Unmarshal(s.Bytes(), &entry) != nil || entry.Message == "" {
			bad++
			continue
		}
		entries = append(entries, entry)
	}
	if err := s.Err(); err != nil {
		return nil, 0, fmt.Errorf("read feedback: %w", err)
	}
	return entries, bad, nil
}

// feedbackPattern is the cheap literal a log has to contain before it is
// parsed at all.
var feedbackPattern = regexp.MustCompile(`feedback\s+add`)

// harvestFeedback reads every note agents left with `agentlog feedback add`,
// from the shell calls in their own logs, across all time.
func harvestFeedback(env Env) ([]feedbackEntry, string, error) {
	cat := env.Catalog
	res, err := cat.Find(env.ctx(), catalog.ParseClues([]string{"all"}, cat.CWD, cat.Now))
	if err != nil && res == nil {
		return nil, "", err
	}
	pool := candidates(res)
	// A lookup leaves out the calling agent's own session; its notes count.
	if _, self := currentAgent(); res.HiddenSelf && self != "" {
		if own, _ := cat.Find(env.ctx(), catalog.ParseClues([]string{self, "all"}, cat.CWD, cat.Now)); own != nil {
			pool = append(pool, candidates(own)...)
		}
	}
	found := scan.Sessions(env.ctx(), pool, feedbackPattern)
	var out []feedbackEntry
	for at := 0; at < len(found.Matches); {
		for _, h := range selectBatch(env, found.Matches[at:], options{}, engine.Filter{}) {
			at++
			if h.t != nil {
				out = append(out, feedbackIn(h.t)...)
			}
		}
	}
	searched := fmt.Sprintf("searched %s", plural(found.Sessions-found.Unread, "session"))
	return out, searched, nil
}

// feedbackIn is the notes one session left, in the order it left them.
func feedbackIn(t *transcript.Transcript) []feedbackEntry {
	var out []feedbackEntry
	for _, e := range t.Events {
		if e.Kind != transcript.KindTool || !shellTool(e.Tool) {
			continue
		}
		for _, msg := range feedbackCalls(e.Input) {
			outcome := ""
			for _, flag := range []string{"--ok", "--fallback"} {
				if strings.HasPrefix(msg, flag+" ") {
					outcome = strings.TrimPrefix(flag, "--")
					msg = strings.TrimPrefix(msg, flag+" ")
					break
				}
			}
			out = append(out, feedbackEntry{
				Outcome:   outcome,
				Time:      e.Time,
				Provider:  t.Session.Provider,
				SessionID: t.Session.ID,
				CWD:       t.Session.CWD,
				Message:   msg,
				Ref:       fmt.Sprintf("%s#%d", render.Ref(&t.Session), e.Index),
			})
		}
	}
	return out
}

// shellTool is the name each harness gives its shell tool.
func shellTool(name string) bool {
	switch name {
	case "Bash", "bash", "shell":
		return true
	}
	return false
}

// mergeFeedback puts both sources in time order, each note once. The same
// words turn up more than once when an agent retried a refused save, when a
// resumed session's log repeats its parent's, and when a note saved to the
// store before notes moved to the logs is in its log as well. The earliest
// copy from a log is kept, since it says where to find the session.
func mergeFeedback(stored, logged []feedbackEntry) []feedbackEntry {
	all := append(append([]feedbackEntry(nil), logged...), stored...)
	sort.SliceStable(all, func(i, j int) bool {
		if (all[i].Ref != "") != (all[j].Ref != "") {
			return all[i].Ref != ""
		}
		return all[i].Time.Before(all[j].Time)
	})
	seen := map[string]bool{}
	var out []feedbackEntry
	for _, e := range all {
		if seen[e.Message] {
			continue
		}
		seen[e.Message] = true
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}
