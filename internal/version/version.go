// Package version carries the build identity of the running agent.
package version

// Version is the release tag this binary was built from, injected at link time
// with -X github.com/nubitio/nubit-agent/internal/version.Version=v1.2.3.
// A source build leaves it "dev", which never self-updates: an unversioned
// binary has no ordering against a release and must not be replaced silently.
var Version = "dev"

// IsRelease reports whether this binary came from a tagged release build.
func IsRelease() bool {
	return Version != "" && Version != "dev"
}
