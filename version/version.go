// Package version holds the binary's build-time version and provides
// semver comparison helpers used by the update check.
package version

import "golang.org/x/mod/semver"

// Current is overridden at build time via
// -ldflags "-X github.com/lyj404/go-monitor/version.Current=v1.2.3".
// It defaults to "dev" for plain `go build`/`go run` invocations.
var Current = "dev"

// IsKnown reports whether Current is a comparable semver. The "dev"
// placeholder and any other unparsable value count as unknown, in which
// case the update check never reports an update.
func IsKnown() bool {
	return semver.IsValid(Current)
}

// HasUpdate reports whether latest is strictly newer than Current.
// Returns false when either version is missing or not valid semver.
func HasUpdate(latest string) bool {
	if !semver.IsValid(Current) || !semver.IsValid(latest) {
		return false
	}
	return semver.Compare(latest, Current) > 0
}
