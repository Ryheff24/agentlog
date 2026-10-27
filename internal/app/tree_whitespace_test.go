package app

import (
	"strings"
	"testing"
)

func TestTreeLinesHaveNoTrailingSpaces(t *testing.T) {
	w := newWorld(t)
	out, code := w.run("/", teamID, "--tree")
	if code != 0 {
		t.Fatal(out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimRight(line, " \t") != line {
			t.Fatalf("trailing whitespace: %q", line)
		}
	}
}
