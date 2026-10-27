package usage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// callerMetadata keeps usage independent of transcript readers. A fork's
// creation-time replay is not new use; a session editing agentlog is development.
func callerMetadata(path, cwd string) (bool, time.Time) {
	root := ""
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidate := filepath.Dir(filepath.Dir(exe))
		if b, err := os.ReadFile(filepath.Join(candidate, "go.mod")); err == nil && strings.HasPrefix(string(b), "module agentlog\n") {
			root = candidate
		}
	}
	inside := func(p string) bool {
		p = filepath.Clean(p)
		return p != "." && (root != "" && (p == root || strings.HasPrefix(p, root+string(filepath.Separator))) || filepath.Base(p) == "agentlog" || strings.Contains(filepath.ToSlash(p), "/agentlog/"))
	}
	dev := false
	var cutoff time.Time
	eachLine(path, func(line []byte) {
		if !bytes.Contains(line, []byte("session_meta")) && !bytes.Contains(line, []byte(`"assistant"`)) && !bytes.Contains(line, []byte("file_change")) && !bytes.Contains(line, []byte("apply_patch")) {
			return
		}
		var r struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Payload   struct {
				Fork      string    `json:"forked_from_id"`
				Timestamp time.Time `json:"timestamp"`
			} `json:"payload"`
			Message struct {
				Content []struct {
					Type  string `json:"type"`
					Name  string `json:"name"`
					Input struct {
						Path     string `json:"file_path"`
						Notebook string `json:"notebook_path"`
					} `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &r) != nil {
			return
		}
		if r.Type == "session_meta" && r.Payload.Fork != "" {
			cutoff = r.Payload.Timestamp
			if cutoff.IsZero() {
				cutoff = r.Timestamp
			}
			cutoff = cutoff.Add(250 * time.Millisecond)
		}
		for _, c := range r.Message.Content {
			switch c.Name {
			case "Write", "Edit", "MultiEdit", "NotebookEdit":
				p := c.Input.Path
				if p == "" {
					p = c.Input.Notebook
				}
				if !filepath.IsAbs(p) {
					p = filepath.Join(cwd, p)
				}
				dev = dev || inside(p)
			}
		}
		// Codex FileChange and apply_patch are recognizable without decoding
		// every tool output. Limit inspection to records describing edits.
		if strings.Contains(string(line), "file_change") || strings.Contains(string(line), "apply_patch") {
			var v any
			if json.Unmarshal(line, &v) == nil {
				dev = dev || editsAgentlog(v, cwd, inside)
			}
		}
	})
	return dev, cutoff
}

func editsAgentlog(v any, cwd string, inside func(string) bool) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if s, ok := v.(string); ok && (k == "path" || k == "file_path") {
				if !filepath.IsAbs(s) {
					s = filepath.Join(cwd, s)
				}
				if inside(s) {
					return true
				}
			}
			if editsAgentlog(v, cwd, inside) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if editsAgentlog(v, cwd, inside) {
				return true
			}
		}
	case string:
		for _, line := range strings.Split(x, "\n") {
			for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
				if strings.HasPrefix(line, prefix) {
					p := strings.TrimPrefix(line, prefix)
					if !filepath.IsAbs(p) {
						p = filepath.Join(cwd, p)
					}
					if inside(p) {
						return true
					}
				}
			}
		}
	}
	return false
}

// Callers is the deduplicated non-development view. The all view stays
// available for auditing development and ground-truth validation.
func Callers(calls []Call, sessions []Session) ([]Call, []Session) {
	var cs []Call
	files := map[string]bool{}
	for _, c := range calls {
		if !c.Development {
			cs = append(cs, c)
			files[c.File] = true
		}
	}
	var ss []Session
	for _, s := range sessions {
		if files[s.File] {
			ss = append(ss, s)
		}
	}
	return cs, ss
}
