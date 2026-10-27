package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseNotesSelectVersion(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 is needed for the release notes check")
	}
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	source := "## Unreleased\n\nLater changes.\n\n## 0.1.0 - 2026-10-21\n\nFirst release.\n\n### Fixed\n\nPortable paths.\n\n## 0.0.1 - 2026-10-01\n\nOlder release.\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version, want string
		fail          bool
	}{
		{"0.1.0", "First release.\n\n### Fixed\n\nPortable paths.\n", false},
		{"0.0.1", "Older release.\n", false},
		{"0.2.0", "Missing dated changelog section", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			out, err := exec.Command(python, "release-notes.py", tc.version, "--changelog", path).CombinedOutput()
			if tc.fail {
				if err == nil || !strings.Contains(string(out), tc.want) {
					t.Fatalf("missing version: %v: %s", err, out)
				}
			} else if err != nil || strings.ReplaceAll(string(out), "\r\n", "\n") != tc.want {
				t.Fatalf("notes: %v: %q", err, out)
			}
		})
	}
}
