//go:build unix

package work

import "syscall"

// alive reports whether a process still exists. Signal 0 performs the
// permission and existence checks and delivers nothing.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
