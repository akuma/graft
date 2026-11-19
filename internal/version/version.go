// Package version is the graft build identity printed by the CLI.
//
// Release builds set Version with -ldflags -X. A local go build leaves it
// as "dev" and String reports the VCS revision from build info instead.
package version

import (
	"runtime/debug"
)

// Version is stamped at link time. "dev" means the binary was not stamped.
var Version = "dev"

// String is the version a person should see.
func String() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	var rev, dirty string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			rev = setting.Value
		case "vcs.modified":
			if setting.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return "dev-" + rev + dirty
}
