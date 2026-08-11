// Package build exposes version information stamped into the executable by the Go toolchain.
package build

import (
	"runtime/debug"
	"strings"
)

const unknownVersion = "dev"

var version string

func Version() string {
	if stamped := strings.TrimSpace(version); stamped != "" {
		return stamped
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownVersion
	}
	if version := strings.TrimSpace(info.Main.Version); version != "" && version != "(devel)" {
		return version
	}
	revision := setting(info, "vcs.revision")
	if revision == "" {
		return unknownVersion
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if setting(info, "vcs.modified") == "true" {
		revision += "+dirty"
	}
	return revision
}

func UserAgent() string {
	return "jvm-switcher/" + Version()
}

func setting(info *debug.BuildInfo, key string) string {
	for _, item := range info.Settings {
		if item.Key == key {
			return item.Value
		}
	}
	return ""
}
