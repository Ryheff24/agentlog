package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The README promises that agentlog only reads: no network, and no other
// programs run on the person's behalf. This holds it to that by what the
// binaries link, so the promise can't drift while the code changes under it.
func TestBinariesCannotReachTheNetwork(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool on PATH")
	}
	out, err := exec.Command(gobin, "list", "-deps", "../...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for pkg := range strings.FieldsSeq(string(out)) {
		if pkg == "net" || strings.HasPrefix(pkg, "net/") || pkg == "crypto/tls" || pkg == "os/exec" {
			t.Errorf("agentlog links %s", pkg)
		}
	}
}
