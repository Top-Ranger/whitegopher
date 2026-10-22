// Package buildinfo reports the version of the running binary.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Version describes this build. linked is the version injected at release time
// with -ldflags, if any.
func Version(linked string) string {
	v := linked
	var rev string
	var dirty bool
	if bi, ok := debug.ReadBuildInfo(); ok {
		// A plain tag from "go install ...@vX.Y.Z"; pseudo-versions and
		// dirty builds fall through to "dev" plus the commit.
		if mv := bi.Main.Version; v == "" && strings.HasPrefix(mv, "v") && !strings.ContainsAny(mv, "-+") {
			v = mv
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if v == "" {
		v = "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" {
		v += " (" + rev
		if dirty {
			v += "+dirty"
		}
		v += ")"
	}
	return v
}
