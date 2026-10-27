//go:build !unix

package private

import "os"

// Ownership and link-free opening aren't checked here; agentlog is built and
// released for macOS and Linux.
const noFollow = 0

func ownedByMe(os.FileInfo) bool { return true }
