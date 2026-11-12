// Package version carries build metadata.
package version

// Version is the semantic version of redlite.
const Version = "0.14.0"

// Commit is overridden at build time with -ldflags.
var Commit = "dev"

// String renders the version and commit for INFO and --version.
func String() string {
	if Commit == "" {
		return Version
	}
	return Version + " (" + Commit + ")"
}
