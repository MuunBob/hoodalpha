// Package buildinfo exposes build metadata injected at link time.
package buildinfo

import "runtime/debug"

// Set with: -ldflags "-X .../buildinfo.Version=v0.1.0 -X .../buildinfo.Commit=abc123"
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Info is the resolved build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"go_version"`
}

// Get resolves build metadata, falling back to VCS stamps from the Go toolchain.
func Get() Info {
	i := Info{Version: Version, Commit: Commit, Date: Date}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return i
	}
	i.GoVersion = bi.GoVersion
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if i.Commit == "" {
				i.Commit = s.Value
			}
		case "vcs.time":
			if i.Date == "" {
				i.Date = s.Value
			}
		}
	}
	return i
}
