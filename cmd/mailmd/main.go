//go:build !mailmd_mock

package main

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/vincentsch/mailmd/internal/cli"
)

// version is set by the release build.
var version = "dev"

func main() {
	os.Exit(cli.Main(cli.Options{Version: currentVersion()}))
}

// currentVersion falls back to the module version, so a binary from
// "go install ...@v0.1.0" reports 0.1.0 instead of dev.
func currentVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && strings.HasPrefix(info.Main.Version, "v") {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return version
}
