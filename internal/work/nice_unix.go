//go:build unix

package work

import "syscall"

// niceness is how far below everything else this process asks to run. A
// person's editor, build and browser all outrank a log reader.
const niceness = 10

func nice() { _ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, niceness) }
