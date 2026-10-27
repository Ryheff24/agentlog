package app

import (
	"fmt"
	"strconv"
	"strings"
)

// options are the parsed view flags; everything else is a clue.
type options struct {
	user, count, brief, content, images, tree, includeHandbacks          bool
	list, edits, files, subagents, json, full, reasoning, branches, help bool
	usage, tools, projects                                               bool

	tail, head, max, width, limit int
	jobs                          int
	kinds, grep, path             string
}

// parseArgs accepts flags anywhere among the clues, as --flag, --flag=value
// or --flag value.
func parseArgs(args []string) (options, []string, error) {
	var o options
	var clues []string
	boolFlags, intFlags, strFlags := flagTables(&o)

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			clues = append(clues, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			clues = append(clues, a)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if p, ok := boolFlags[name]; ok {
			*p = !hasValue || value != "false"
			continue
		}
		next := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("--%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		if p, ok := intFlags[name]; ok {
			v, err := next()
			if err != nil {
				return o, nil, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return o, nil, fmt.Errorf("--%s wants a number, got %q", name, v)
			}
			*p = n
			continue
		}
		if p, ok := strFlags[name]; ok {
			v, err := next()
			if err != nil {
				return o, nil, err
			}
			*p = v
			continue
		}
		return o, nil, fmt.Errorf("unknown flag %s", a)
	}
	return o, clues, nil
}

// flagTables names every flag the parser accepts, bound to o. The usage text
// is tested against it, so a flag cannot be added without being documented.
func flagTables(o *options) (map[string]*bool, map[string]*int, map[string]*string) {
	boolFlags := map[string]*bool{
		"list": &o.list, "l": &o.list, "edits": &o.edits, "diff": &o.edits, "files": &o.files,
		"subagents": &o.subagents, "json": &o.json, "full": &o.full, "reasoning": &o.reasoning,
		"branches": &o.branches, "help": &o.help, "h": &o.help, "usage": &o.usage, "tools": &o.tools,
		"projects": &o.projects, "user": &o.user, "count": &o.count, "brief": &o.brief,
		"content": &o.content, "images": &o.images, "tree": &o.tree, "include-handbacks": &o.includeHandbacks,
	}
	intFlags := map[string]*int{"tail": &o.tail, "head": &o.head, "max": &o.max, "width": &o.width, "limit": &o.limit, "n": &o.limit, "jobs": &o.jobs}
	strFlags := map[string]*string{"kind": &o.kinds, "grep": &o.grep, "path": &o.path}
	return boolFlags, intFlags, strFlags
}

// wantsView reports whether a flag asks for something other than the preview.
func (o options) wantsView() bool {
	return o.brief || o.user || o.count || o.images || o.tree || o.edits || o.files || o.usage || o.tools || o.tail > 0 || o.head > 0 || o.kinds != "" || o.grep != ""
}
