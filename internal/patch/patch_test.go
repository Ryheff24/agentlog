package patch

import "testing"

func TestParseEnvelope(t *testing.T) {
	in := `cat <<'EOF' | apply_patch
*** Begin Patch
*** Update File: src/player.ts
@@ class Player
-  const full = false
+  const full = true
+  log("fullscreen")
*** Add File: tests/legacy-api.mjs
+import test from "node:test"
*** Delete File: old.js
*** Update File: a.ts
*** Move to: b.ts
@@
-x
+y
*** End Patch
EOF`
	got := ParseEnvelope(in)
	want := []struct {
		op, path, moveTo string
		added, removed   int
	}{
		{"update", "src/player.ts", "", 2, 1},
		{"add", "tests/legacy-api.mjs", "", 1, 0},
		{"delete", "old.js", "", 0, 0},
		{"move", "a.ts", "b.ts", 1, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseEnvelope() returned %d changes, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Op != w.op || g.Path != w.path || g.MoveTo != w.moveTo || g.Added != w.added || g.Removed != w.removed {
			t.Errorf("change %d = %s %s→%s +%d -%d, want %s %s→%s +%d -%d",
				i, g.Op, g.Path, g.MoveTo, g.Added, g.Removed, w.op, w.path, w.moveTo, w.added, w.removed)
		}
	}
}

func TestParseEnvelopeWithoutMarker(t *testing.T) {
	if got := ParseEnvelope("just some text"); got != nil {
		t.Errorf("ParseEnvelope() = %+v, want nil", got)
	}
}

func TestCountSkipsFileHeaders(t *testing.T) {
	diff := "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n-old\n+new\n+more\n ctx"
	if a, r := Count(diff); a != 2 || r != 1 {
		t.Errorf("Count() = +%d -%d, want +2 -1", a, r)
	}
}

func TestReplaceAndWrite(t *testing.T) {
	c := Replace("f.go", "a\nb\n", "c\n")
	if c.Added != 1 || c.Removed != 2 || c.Diff != "@@\n-a\n-b\n+c" {
		t.Errorf("Replace() = %+v", c)
	}
	w := Write("new.go", "package x\n\nfunc F() {}\n")
	if w.Op != "write" || w.Added != 3 || w.Removed != 0 {
		t.Errorf("Write() = %+v", w)
	}
}

func TestFromHunks(t *testing.T) {
	c := FromHunks("f.go", "update", []Hunk{{OldStart: 3, OldLines: 2, NewStart: 3, NewLines: 2, Lines: []string{" ctx", "-old", "+new"}}})
	if c.Diff != "@@ -3,2 +3,2 @@\n ctx\n-old\n+new" || c.Added != 1 || c.Removed != 1 {
		t.Errorf("FromHunks() = %+v", c)
	}
}
