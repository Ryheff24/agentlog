//go:build !unix

package work

// alive cannot be answered cheaply here, so a claim is trusted until it goes
// stale. Claims are removed when a process exits normally.
func alive(int) bool { return true }
