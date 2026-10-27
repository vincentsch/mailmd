package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemo(t *testing.T) {
	config, out := t.TempDir(), filepath.Join(t.TempDir(), "demo-mail")
	code, stdout, _ := invoke(t, Options{ConfigRoot: config}, "", "demo", "--out", out, "--dry-run")
	if _, err := os.Stat(out); code != 0 || !strings.Contains(stdout, "would write 8 messages") || !os.IsNotExist(err) {
		t.Fatalf("dry run: %d %s %v", code, stdout, err)
	}
	code, stdout, stderr := invoke(t, Options{ConfigRoot: config}, "", "demo", "--out", out)
	if code != 0 || !strings.Contains(stdout, "Wrote 8 messages of sample mail") {
		t.Fatalf("demo: %d\n%s\n%s", code, stdout, stderr)
	}
	files := map[string]bool{}
	_ = filepath.WalkDir(filepath.Join(out, "demo"), func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == ".mailmd" {
			return filepath.SkipDir
		}
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(filepath.Join(out, "demo"), path)
			files[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	var pdf, spam bool
	for f := range files {
		pdf = pdf || strings.HasSuffix(f, ".pdf")
		spam = spam || strings.HasPrefix(strings.ToLower(f), "spam/")
	}
	if !files["index.jsonl"] || !pdf || spam {
		t.Fatalf("unexpected demo files: %v", files)
	}
	if entries, _ := os.ReadDir(config); len(entries) != 0 {
		t.Fatal("the demo connected an account")
	}

	if code, stdout, _ := invoke(t, Options{ConfigRoot: config}, "", "demo", "--out", out); code != 0 || !strings.Contains(stdout, "The sample mail is already in") {
		t.Fatalf("demo run on an existing folder: %d\n%s", code, stdout)
	}
	code, stdout, _ = invoke(t, Options{ConfigRoot: config}, "", "demo", "--out", out, "--json")
	var result DemoResult
	if code != 0 || json.Unmarshal([]byte(stdout), &result) != nil || result.Schema != "mailmd-demo/1" || result.New != 0 || len(result.Files) != len(files) {
		t.Fatalf("second demo run: %d %s", code, stdout)
	}
}
