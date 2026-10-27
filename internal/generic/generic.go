// Package generic reads a record whose schema nobody has written down.
//
// It is the floor the three readers stand on. Each of them knows the shapes
// its provider writes today, and each of those tables goes stale the week the
// provider changes something. When that happens the record arrives here, and
// what comes out is an event: in the right place, with a number, carrying the
// record's content. The fidelity is worse. Nothing is missing.
//
// It works by shape, not by name. A record with something that looks like a
// call id, something that looks like arguments and something that looks like
// output is a tool call whatever it is called this month, and saying so is
// most of what a reader wanted from the table entry that no longer exists.
package generic

import (
	"strconv"
	"strings"
	"time"

	"agentlog/internal/flat"
	"agentlog/internal/transcript"
)

// Event builds the best event that can be made of a record without knowing
// what it is. recordType is what the reader called it when it gave up.
func Event(recordType string, raw []byte, at time.Time, line int) transcript.Event {
	e := transcript.Generic(recordType, "", at, line)
	e.Line = line

	var input, output []string
	for _, l := range flat.Lines(raw, flat.Options{MaxValue: fieldBytes, MaxLines: recordLines}) {
		if l.Value == "" || l.Value == "(empty)" || l.Value == "null" {
			continue
		}
		switch role(l.Path) {
		case roleID:
			if e.CallID == "" {
				e.CallID = l.Value
			}
		case roleName:
			if e.Tool == "" {
				e.Tool = l.Value
			}
		case roleInput:
			input = append(input, label(l.Path)+l.Value)
		case roleOutput:
			output = append(output, label(l.Path)+l.Value)
		case roleExit:
			if n, err := strconv.Atoi(l.Value); err == nil && e.ExitCode == nil {
				e.ExitCode = &n
				e.Failed = e.Failed || n != 0
			}
		case roleFailed:
			e.Failed = e.Failed || truthy(l.Value)
		case roleTime:
			if e.Time.IsZero() {
				if t, err := time.Parse(time.RFC3339, l.Value); err == nil {
					e.Time = t
				}
			}
		}
	}

	// A name and something it did: that is a tool call, whatever it is called
	// this month. Anything less stays a record, shown whole.
	if e.Tool != "" && (len(input) > 0 || len(output) > 0) {
		e.Kind = transcript.KindTool
		e.Input = strings.Join(input, "\n")
		e.Output = strings.Join(output, "\n")
		if e.Output != "" {
			// The id is only worth carrying while the result is still
			// missing; a reader holding both has no pairing left to do.
			e.CallID = ""
		}
		return e
	}
	e.Kind = transcript.KindRecord
	e.Tool, e.CallID = "", ""
	e.Text = flat.Record(raw)
	return e
}

const (
	// fieldBytes bounds one field of a record read this way.
	fieldBytes = 600
	// recordLines bounds how deep into a record the shape reading goes.
	recordLines = 60
)

type kind int

const (
	roleNone kind = iota
	roleID
	roleName
	roleInput
	roleOutput
	roleExit
	roleFailed
	roleTime
)

// roles maps the field names every harness has converged on, by last path
// segment. This is the one table in the package, and it is about JSON as a
// genre rather than about any provider: adding a name to it is a line.
var roles = map[string]kind{
	"id": roleID, "call_id": roleID, "callid": roleID, "tool_call_id": roleID,
	"toolcallid": roleID, "tool_use_id": roleID, "invocation_id": roleID,

	"name": roleName, "tool": roleName, "tool_name": roleName, "toolname": roleName,
	"function": roleName, "function_name": roleName, "command_name": roleName,

	"command": roleInput, "cmd": roleInput, "arguments": roleInput, "args": roleInput,
	"input": roleInput, "params": roleInput, "parameters": roleInput, "query": roleInput,
	"prompt": roleInput, "path": roleInput, "file_path": roleInput, "filepath": roleInput,
	"url": roleInput, "pattern": roleInput, "description": roleInput,

	"output": roleOutput, "result": roleOutput, "results": roleOutput, "stdout": roleOutput,
	"stderr": roleOutput, "content": roleOutput, "text": roleOutput, "message": roleOutput,
	"response": roleOutput, "aggregated_output": roleOutput, "formatted_output": roleOutput,
	"body": roleOutput, "summary": roleOutput, "reason": roleOutput,

	"exit_code": roleExit, "exitcode": roleExit, "status_code": roleExit, "returncode": roleExit,

	"is_error": roleFailed, "iserror": roleFailed, "error": roleFailed, "failed": roleFailed,
	"interrupted": roleFailed, "aborted": roleFailed, "success": roleNone,

	"timestamp": roleTime, "time": roleTime, "created_at": roleTime, "started_at": roleTime,
}

// role classifies one leaf by its last path segment, then by its parent: a
// record often puts the interesting part one level down, as input.command or
// result.stdout, and the parent is what says which it is.
func role(path string) kind {
	segs := strings.FieldsFunc(path, func(r rune) bool { return r == '.' || r == '[' })
	for i := len(segs) - 1; i >= 0 && i >= len(segs)-2; i-- {
		key := strings.ToLower(strings.TrimSuffix(segs[i], "]"))
		if _, err := strconv.Atoi(key); err == nil {
			continue // an array index says nothing
		}
		if k, ok := roles[key]; ok && k != roleNone {
			return k
		}
	}
	return roleNone
}

// label prefixes a value with its field name when the name adds something the
// value does not already say.
func label(path string) string {
	key := path
	if i := strings.LastIndexAny(key, ".["); i >= 0 {
		key = key[i+1:]
	}
	key = strings.TrimSuffix(key, "]")
	switch strings.ToLower(key) {
	case "command", "output", "stdout", "text", "content", "input", "result":
		return ""
	}
	return key + ": "
}

func truthy(v string) bool {
	switch strings.ToLower(v) {
	case "true", "1":
		return true
	}
	// An error field with a message in it is an error.
	return len(v) > 1 && v != "false" && v != "0" && v != "none"
}
