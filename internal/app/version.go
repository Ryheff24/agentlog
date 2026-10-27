package app

import "runtime/debug"

// Version is set by release builds:
//
//	go build -ldflags "-X github.com/Ryheff24/agentlog/internal/app.Version=v0.1.0" ./cmd/agentlog
//
// Without it, Go's own stamp is used: the module version for
// `go install …@v0.1.0`, a commit-based one for a build from a checkout.
var Version = ""

func version() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}
