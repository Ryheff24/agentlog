package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if code := Run(Env{Stdout: &out, Stderr: &out}, []string{"--version"}); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.HasPrefix(out.String(), "agentlog devel") {
		t.Fatalf("unstamped build: %q", out.String())
	}

	defer func(v string) { Version = v }(Version)
	Version = "v1.2.3"
	out.Reset()
	Run(Env{Stdout: &out, Stderr: &out}, []string{"--version"})
	if out.String() != "agentlog v1.2.3\n" {
		t.Fatalf("stamped build: %q", out.String())
	}
}
