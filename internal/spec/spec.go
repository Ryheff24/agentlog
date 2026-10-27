// Package spec is where each reader writes down what it knows, as a table
// rather than as control flow.
//
// The reason is maintenance under drift. A provider renames a record on a
// Tuesday; the fix should be a line in a table that anyone can see is a line
// in a table, not a case buried in a five-hundred-line switch that only the
// person who wrote it can find. The tables are also what `agentlog doctor`
// reports against, and what the tests check the readers against, so a table
// that stops matching its reader fails a test instead of quietly lying.
//
// A table entry does not do the parsing. It declares that the reader has a
// case for a type and says what a reader of the table should expect that type
// to become — which is exactly the knowledge that goes stale.
package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Role is what a record type becomes when a reader has a case for it.
type Role string

const (
	Step   Role = "step"   // becomes an event: a message, a call, an edit
	Meta   Role = "meta"   // sets something about the session, produces no event
	Ignore Role = "ignore" // deliberately produces nothing, and is counted
)

// Record is one record type a reader knows.
type Record struct {
	Type string `json:"type"` // as the reader names it, e.g. "event_msg:token_count"
	Role Role   `json:"role"`
	Note string `json:"note,omitempty"`
}

// Class is what kind of thing a tool is, for readers that group tools.
type Class string

const (
	Shell Class = "shell"
	Edit  Class = "edit"
	Read  Class = "read"
	Agent Class = "agent"
	Web   Class = "web"
	MCP   Class = "mcp"
)

// Table is one harness's declared knowledge.
type Table struct {
	Provider string
	Records  []Record
	Tools    map[string]Class
}

// Knows reports whether the table has an entry for a record type.
func (t Table) Knows(recordType string) bool {
	for _, r := range t.Records {
		if r.Type == recordType {
			return true
		}
	}
	return false
}

// tables is filled by each reader at init, so that nothing imports a reader in
// order to ask what it knows.
var tables = map[string]Table{}

// Register is called by a reader with its own table.
func Register(t Table) { tables[t.Provider] = t }

// Of returns a harness's table, merged with whatever the user has added.
func Of(provider string) Table {
	t := tables[provider]
	for _, r := range user()[provider] {
		if !t.Knows(r.Type) {
			t.Records = append(t.Records, r)
		}
	}
	return t
}

// UserDeclared reports whether the person declared a record type in
// types.json. Such a type is still read by shape, but it is known: it is
// counted as passed over, not as drift.
func UserDeclared(provider, recordType string) bool {
	for _, r := range user()[provider] {
		if r.Type == recordType {
			return true
		}
	}
	return false
}

// user is the extension point: a file the person can write to teach this build
// about a record type it does not know, without waiting for a new build.
//
// What it can do today is stop a type being reported as drift, and name it in
// the views — the reading itself is still by shape. That is the whole of what
// a table entry gives, which is why it is the shape of the hook: when the
// readers consult the tables for their field paths as well, the same file will
// carry those.
//
//	~/.config/agentlog/types.json
//	{"copilot": [{"type": "session.warning", "role": "ignore", "note": "MCP warnings"}]}
func user() map[string][]Record {
	path := typesPath()
	userMu.Lock()
	defer userMu.Unlock()
	if path == userPath {
		return userRecords
	}
	userPath, userRecords = path, map[string][]Record{}
	b, err := os.ReadFile(path)
	if err != nil {
		return userRecords
	}
	var got map[string][]Record
	if json.Unmarshal(b, &got) != nil {
		return userRecords
	}
	for provider, rs := range got {
		for i := range rs {
			rs[i].Note = strings.TrimSpace(rs[i].Note + " (from " + filepath.Base(path) + ")")
		}
		userRecords[provider] = rs
	}
	return userRecords
}

func typesPath() string {
	if dir := os.Getenv("AGENTLOG_CONFIG"); dir != "" {
		return filepath.Join(dir, "types.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "agentlog", "types.json")
}

// The file is read again only when its path changes.
var (
	userMu      sync.Mutex
	userPath    = "\x00" // no path yet
	userRecords map[string][]Record
)
