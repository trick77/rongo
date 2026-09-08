// Package version carries the build's own version, and nothing else.
//
// It is set at link time — the release workflow computes the next tag before it
// builds, and passes it in as a build arg; nothing here can read it back out of
// git, because the tag is created only after the image push succeeds, so a
// `git describe` inside the build would report the PREVIOUS release.
//
// "dev" is what an unstamped build says: `make build` without VERSION, `go run`,
// and every test binary. The UI treats it as no version at all and says only
// that Rongo can be wrong.
package version

var Version = "dev"
