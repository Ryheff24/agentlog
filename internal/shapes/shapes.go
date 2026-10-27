// Package shapes keeps one redacted example of every record shape the readers
// have ever met, so that drift is a failing test rather than a discovery.
//
// A shape is a record type plus the set of field paths it carries. Providers
// change formats by adding a field, renaming one, moving one a level down —
// each of those is a new shape, and each is something a reader may silently
// stop understanding. Harvesting one example of each, from real logs, gives
// the readers a corpus to be tested against that grows as the providers move.
//
// Nothing harvested here may carry anyone's words. Every string is replaced by
// a placeholder except the handful of keys whose values are structural — the
// type names the readers switch on — and paths are rewritten to /Users/me.
// What survives is the shape, which is all a reader is being tested on.
package shapes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Ryheff24/agentlog/internal/flat"
)

// Shape identifies one record layout.
type Shape struct {
	Provider string          `json:"provider"`
	Version  string          `json:"version,omitempty"`
	Type     string          `json:"type"`
	Key      string          `json:"key"`    // provider/version/type/fields digest
	Record   json.RawMessage `json:"record"` // the redacted record
}

// Of describes a record: the type it announces, the CLI version in force when
// it was written, and a digest of the field paths it carries. The digest is
// what makes a shape: two records of the same type with different fields are
// two shapes, because they are two things a reader can fail at.
//
// version is what earlier records in the same file said; a record that names a
// version itself overrides it.
func Of(provider, version string, raw []byte) Shape {
	var (
		paths []string
		types []flat.Line
	)
	for _, l := range flat.Lines(raw, flat.Options{MaxValue: typeBytes, MaxLines: maxPaths}) {
		p := generalise(unindex(l.Path))
		paths = append(paths, p)
		switch {
		case isType(p, l.Path):
			types = append(types, flat.Line{Path: p, Value: l.Value})
		case isVersion(p) && version != l.Value && looksLikeVersion(l.Value):
			version = l.Value
		}
	}
	typ := name(types)
	if typ == "" {
		typ = "untyped"
	}
	return Shape{
		Provider: provider,
		Version:  version,
		Type:     typ,
		Key:      fmt.Sprintf("%s/%s/%s", provider, typ, digest(paths)),
		Record:   Redact(raw),
	}
}

// digest fingerprints a record's structure. Paths are cut off below a few
// levels: what a reader switches on lives near the top, and keeping every leaf
// of every tool's output would make each tool call its own shape and the
// fixtures useless. The record itself is kept whole, so the depth is only
// about what counts as the same shape twice.
//
// The version is deliberately not part of it. A new CLI version that writes
// the same fields is not a new thing for a reader to fail at; one that writes
// different fields shows up here as a new shape, which is the point.
func digest(paths []string) string {
	seen := make(map[string]bool, len(paths))
	trimmed := make([]string, 0, len(paths))
	for _, p := range paths {
		p = cut(p, maxDepth)
		if !seen[p] {
			seen[p], trimmed = true, append(trimmed, p)
		}
	}
	sort.Strings(trimmed)
	sum := sha256.Sum256([]byte(strings.Join(trimmed, "\n")))
	return hex.EncodeToString(sum[:4])
}

// cut keeps the first depth segments of a path.
func cut(path string, depth int) string {
	n := 0
	for i, r := range path {
		if r == '.' {
			if n++; n == depth {
				return path[:i]
			}
		}
	}
	return path
}

const (
	// typeBytes is enough of a value to read a type name or a version out of
	// it, and not enough to carry a sentence by accident.
	typeBytes = 80
	// maxPaths bounds how much of one record's structure is digested.
	maxPaths = 500
	// maxItems is how many elements of an array survive redaction. A record
	// with four hundred content blocks has the same shape as one with three.
	maxItems = 8
	// maxDepth is how far into a record two shapes have to differ to count as
	// two shapes.
	maxDepth = 4
)

// name joins the type names a record announces, outermost first, so that a
// record is called the same thing however its writer happened to order the
// keys: "event_msg:item_completed:CommandExecution".
func name(types []flat.Line) string {
	sort.SliceStable(types, func(i, j int) bool {
		a, b := types[i].Path, types[j].Path
		if da, db := strings.Count(a, "."), strings.Count(b, "."); da != db {
			return da < db
		}
		return a < b
	})
	var out []string
	for _, t := range types {
		if t.Value != "" {
			out = append(out, t.Value)
		}
	}
	return strings.Join(out, ":")
}

// isType reports whether a leaf names the record's type. Only the outer
// levels count: a type inside an array is a content block's type, which is
// part of the shape digest already and would make the name unwieldy.
func isType(path, raw string) bool {
	if strings.ContainsAny(raw, "[") || !strings.HasSuffix(path, "type") {
		return false
	}
	seg := strings.Split(path, ".")
	return len(seg) <= 3 && seg[len(seg)-1] == "type"
}

func isVersion(path string) bool {
	switch strings.ToLower(path[strings.LastIndexByte(path, '.')+1:]) {
	case "version", "cli_version", "copilotversion":
		return true
	}
	return false
}

// looksLikeVersion keeps a schema version number ("1") or a git sha out of the
// place a CLI version belongs.
func looksLikeVersion(v string) bool {
	return strings.Count(v, ".") >= 2 && len(v) < 24 && v[0] >= '0' && v[0] <= '9'
}

// contentKey reports a map key that is somebody's words rather than a field
// name. Field names are identifiers; a key with a space, punctuation or the
// length of a sentence is content.
func contentKey(k string) bool {
	if len(k) > 64 {
		return true
	}
	for _, r := range k {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		case r == '_', r == '-', r == '.', r == ':', r == '@', r == '$':
		default:
			return true
		}
	}
	return false
}

// generalise replaces the segments of a path that are content keys, so a
// record keyed by a question has one shape, not one per question asked.
func generalise(path string) string {
	seg := strings.Split(path, ".")
	for i, s := range seg {
		if contentKey(s) {
			seg[i] = "*"
		}
	}
	return strings.Join(seg, ".")
}

// unindex removes array positions, so a record with two content blocks has the
// same shape as one with five.
func unindex(path string) string {
	for {
		i := strings.IndexByte(path, '[')
		if i < 0 {
			return path
		}
		j := strings.IndexByte(path[i:], ']')
		if j < 0 {
			return path[:i]
		}
		path = path[:i] + path[i+j+1:]
	}
}

// Redact rewrites a record so that it carries its shape and nobody's words.
// Every string becomes a placeholder unless its key is structural; numbers
// become 1; paths become paths under /Users/me.
func Redact(raw []byte) []byte {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return []byte(`{"unparseable":true}`)
	}
	out, err := json.Marshal(scrub("", v))
	if err != nil {
		return []byte(`{"unmarshalable":true}`)
	}
	return out
}

func scrub(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			// Some records key a map by a path — which is content wearing a
			// field name, and has to go the same way as any other content.
			if strings.ContainsAny(k, "/\\") {
				out[scrubString("path", k)] = scrub("", val)
				continue
			}
			// Others key a map by a sentence: Claude's AskUserQuestion result
			// keys each answer by the question asked. A key like that is
			// replaced by a stable stand-in, so two questions stay two keys.
			if contentKey(k) {
				sum := sha256.Sum256([]byte(k))
				out["key-"+hex.EncodeToString(sum[:4])] = scrub("", val)
				continue
			}
			out[k] = scrub(k, val)
		}
		return out
	case []any:
		if len(t) > maxItems {
			t = t[:maxItems]
		}
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = scrub(key, val)
		}
		return out
	case string:
		return scrubString(key, t)
	case float64:
		if t == 0 {
			return 0
		}
		return 1
	default:
		return v
	}
}

// structural names the keys whose values the readers switch on. Their values
// are format, not content, and a fixture without them tests nothing.
var structural = map[string]bool{
	"type": true, "subtype": true, "role": true, "kind": true, "status": true,
	"operation": true, "event": true, "name": true, "tool_name": true,
	"toolname": true, "warningtype": true, "errortype": true, "producer": true,
	"originator": true, "cli_version": true, "version": true, "copilotversion": true,
	"agent_role": true, "thread_source": true, "model": true, "mode": true,
}

// idLike names the keys whose values must stay distinct within a record for
// the reader's pairing to be exercised, but must not be anyone's real id.
var idLike = map[string]bool{
	"id": true, "uuid": true, "parentuuid": true, "logicalparentuuid": true,
	"call_id": true, "callid": true, "tool_use_id": true, "tool_call_id": true,
	"toolcallid": true, "thread_id": true, "session_id": true, "sessionid": true,
	"invocation_id": true, "turn_id": true, "item_id": true,
}

func scrubString(key, s string) string {
	k := strings.ToLower(key)
	switch {
	case structural[k]:
		return s
	case idLike[k]:
		// Stable per value, so two fields that referred to the same call still
		// do, and pairing is still exercised.
		sum := sha256.Sum256([]byte(s))
		return "id-" + hex.EncodeToString(sum[:4])
	case strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/"):
		return "/Users/me/project/file" + ext(s)
	case isTimestamp(s):
		return "2026-01-01T00:00:00.000Z"
	case s == "":
		return ""
	default:
		return placeholder(len(s))
	}
}

// ext keeps a path's extension and nothing else. A reader that branches on
// ".go" or ".md" still has something to branch on; where the person keeps
// their work does not travel into a test fixture.
func ext(p string) string {
	e := filepath.Ext(p)
	if len(e) > 8 {
		return ""
	}
	for _, r := range e[min(1, len(e)):] {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9') {
			return ""
		}
	}
	return e
}

// placeholder stands in for text, keeping only roughly how much there was.
func placeholder(n int) string {
	switch {
	case n > 4096:
		return "text-" + strconv.Itoa(n/1024) + "kb"
	case n > 64:
		return "text-" + strconv.Itoa(n)
	default:
		return "text"
	}
}

func isTimestamp(s string) bool {
	return len(s) >= 20 && len(s) <= 30 && s[4] == '-' && s[7] == '-' && (s[10] == 'T' || s[10] == ' ')
}
