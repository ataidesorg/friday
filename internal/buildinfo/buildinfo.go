// Package buildinfo exposes the version stamped at build time.
package buildinfo

import "runtime/debug"

// Set with -ldflags "-X github.com/ataidesorg/friday/internal/buildinfo.Version=v0.1.0 -X ...Commit=abc123".
var (
	Version = "dev"
	Commit  = "unknown"
)

// Summary renders "friday <version> (commit <commit>)", reading the VCS
// revision from the Go build info when no commit was stamped.
func Summary() string {
	commit := Commit
	if commit == "unknown" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 {
					commit = s.Value[:7]
				}
			}
		}
	}
	return "friday " + Version + " (commit " + commit + ")"
}
