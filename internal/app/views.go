package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"agentlog/internal/catalog"
	"agentlog/internal/engine"
	"agentlog/internal/jsonl"
	"agentlog/internal/render"
	"agentlog/internal/transcript"
)

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func countView(w io.Writer, events, sessions int, asJSON bool) error {
	if asJSON {
		return writeJSON(w, struct {
			Events   int `json:"matching_events"`
			Sessions int `json:"matching_sessions"`
		}{events, sessions})
	}
	_, err := fmt.Fprintf(w, "%d matching events in %d sessions\n", events, sessions)
	return err
}

func sessionTree(env Env, s *transcript.Session, o options) error {
	if o.json {
		return writeJSON(env.Stdout, s)
	}
	seen := map[string]bool{}
	var visit func(*transcript.Session, int)
	visit = func(s *transcript.Session, depth int) {
		if seen[s.ID] {
			return
		}
		seen[s.ID] = true
		name := ""
		if s.AgentName != "" {
			name = " " + s.AgentName
		}
		fmt.Fprintf(env.Stdout, "%s%s %s%s%s\n", strings.Repeat("  ", depth), s.Provider, render.Ref(s), name, render.Own(s))
		for _, c := range s.Children {
			visit(c, depth+1)
		}
	}
	if s.ParentID != "" {
		fmt.Fprintf(env.Stdout, "parent: %s\n", s.ParentID)
	}
	visit(s, 0)
	var inline func(int, int)
	inline = func(parent, depth int) {
		for _, a := range s.InlineAgents {
			if a.ParentEvent != parent {
				continue
			}
			name := ""
			if a.Name != "" {
				name = " " + a.Name
			}
			view := ""
			if a.Subagents {
				view = " --subagents"
			}
			fmt.Fprintf(env.Stdout, "%s%s %s#%d%s%s (inline task)\n", strings.Repeat("  ", depth), s.Provider, render.Ref(s), a.Event, view, name)
			inline(a.Event, depth+1)
		}
	}
	inline(0, 1)
	return nil
}

type imageFile struct {
	Ref  string `json:"ref"`
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
}

// imageView reads only the sourced records for the selected events. Blobs
// never enter output; inline images are materialized in the writable temp dir.
func readImages(t *transcript.Transcript, events []transcript.Event) ([]imageFile, error) {
	type source struct {
		path string
		line int
	}
	refs := map[source]string{}
	for _, e := range events {
		path := e.Source
		if path == "" {
			path = t.Session.File()
		}
		refs[source{path, e.Line}] = fmt.Sprintf("%s#%d", render.Ref(&t.Session), e.Index)
		if e.ResultLine > 0 {
			refs[source{e.ResultSource, e.ResultLine}] = fmt.Sprintf("%s#%d", render.Ref(&t.Session), e.Index)
		}
	}
	paths := map[string]bool{}
	for src := range refs {
		paths[src.path] = true
	}
	var ordered []string
	for p := range paths {
		ordered = append(ordered, p)
	}
	sort.Strings(ordered)
	var images []imageFile
	for _, path := range ordered {
		if err := jsonl.Each(path, func(raw []byte, n int) error {
			ref, ok := refs[source{path, n}]
			if !ok {
				return nil
			}
			var record any
			if err := json.Unmarshal(raw, &record); err != nil {
				return err
			}
			return walkImages(record, func(value, media string) error {
				img := imageFile{Ref: ref}
				if strings.HasPrefix(value, "data:") {
					header, data, ok := strings.Cut(value, ",")
					if !ok || !strings.HasSuffix(header, ";base64") {
						return nil
					}
					media = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
					value = data
				}
				if strings.HasPrefix(media, "image/") {
					data, err := base64.StdEncoding.DecodeString(value)
					if err != nil {
						return fmt.Errorf("%s image: %w", ref, err)
					}
					ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}[media]
					if ext == "" {
						ext = ".image"
					}
					f, err := os.CreateTemp("", "agentlog-image-*"+ext)
					if err != nil {
						return err
					}
					_, err = f.Write(data)
					closeErr := f.Close()
					if err != nil {
						return err
					}
					if closeErr != nil {
						return closeErr
					}
					img.Path = f.Name()
				} else if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
					img.URL = value
				} else {
					img.Path = transcript.Abs(value, t.Session.CWD)
				}
				images = append(images, img)
				return nil
			})
		}); err != nil {
			return nil, err
		}
	}
	return images, nil
}

func printImages(env Env, images []imageFile, o options) error {
	if o.json {
		return writeJSON(env.Stdout, images)
	}
	for _, img := range images {
		fmt.Fprintf(env.Stdout, "%s image: %s%s\n", img.Ref, img.Path, img.URL)
	}
	if len(images) == 0 {
		fmt.Fprintln(env.Stdout, "no images in the selected records")
	}
	return nil
}

func walkImages(v any, emit func(string, string) error) error {
	switch x := v.(type) {
	case []any:
		for _, child := range x {
			if err := walkImages(child, emit); err != nil {
				return err
			}
		}
	case map[string]any:
		str := func(k string) string { s, _ := x[k].(string); return s }
		switch str("type") {
		case "image":
			if src, ok := x["source"].(map[string]any); ok {
				data, _ := src["data"].(string)
				media, _ := src["media_type"].(string)
				if data != "" {
					return emit(data, media)
				}
				url, _ := src["url"].(string)
				if url != "" {
					return emit(url, "")
				}
			}
		case "local_image":
			if str("path") != "" {
				return emit(str("path"), "")
			}
		case "input_image", "image_url":
			if str("image_url") != "" {
				return emit(str("image_url"), "")
			}
			if str("url") != "" {
				return emit(str("url"), "")
			}
			if image, ok := x["image_url"].(map[string]any); ok {
				if url, ok := image["url"].(string); ok {
					return emit(url, "")
				}
			}
		}
		// Some tool outputs carry serialized MCP content.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := x[k]
			if k == "images" || k == "local_images" {
				if items, ok := child.([]any); ok {
					for _, item := range items {
						if path, ok := item.(string); ok && path != "" {
							if err := emit(path, ""); err != nil {
								return err
							}
						}
					}
				}
			}
			if k == "output" || k == "content" {
				if s, ok := child.(string); ok && strings.HasPrefix(strings.TrimSpace(s), "{") {
					var decoded any
					if json.Unmarshal([]byte(s), &decoded) == nil {
						child = decoded
					}
				}
			}
			if err := walkImages(child, emit); err != nil {
				return err
			}
		}
	}
	return nil
}

func imageView(env Env, t *transcript.Transcript, events []transcript.Event, o options) error {
	images, err := readImages(t, events)
	if err != nil {
		return err
	}
	return printImages(env, images, o)
}

// imageSessions collects first, so JSON remains one labelled array across
// explicit sessions and separately logged children.
func imageSessions(env Env, res *catalog.Result, clues catalog.Clues, o options) error {
	matches := res.Matches
	if len(clues.IDs) < 2 {
		matches = matches[:1]
	}
	var images []imageFile
	for _, match := range matches {
		t, err := env.Catalog.Load(match.Session, loadOpts(o))
		if err != nil {
			return err
		}
		ts := []*transcript.Transcript{t}
		if o.subagents {
			ts = append(ts, loadChildren(env.Catalog, match.Session, loadOpts(o))...)
		}
		for _, tt := range ts {
			events := tt.Events
			if clues.Event > 0 {
				to := clues.EventTo
				if to < 0 {
					to = tt.LastIndex()
				}
				events = tt.Range(clues.Event, to)
			}
			if o.kinds != "" || o.grep != "" || o.head > 0 || o.tail > 0 {
				f, err := filter(o)
				if err != nil {
					return err
				}
				f.Tail = o.tail
				part := *tt
				part.Events = events
				events = engine.Select(&part, f).Events
			}
			found, err := readImages(tt, events)
			if err != nil {
				return err
			}
			images = append(images, found...)
		}
	}
	return printImages(env, images, o)
}
