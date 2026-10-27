package sync

import (
	"testing"
	"time"

	"github.com/vincentsch/mailmd/internal/backend"
)

func TestFilterMatchesFoldersAndParents(t *testing.T) {
	f := Filter{Exclude: []string{"Newsletters", "work/*"}}
	for folder, want := range map[string]bool{
		"Newsletters":        true,
		"newsletters/Go":     true,
		"Work":               false,
		"Work/Clients":       true,
		"Work/Clients/2026":  true,
		"INBOX":              false,
		"Newsletters Backup": false,
	} {
		if got, _ := f.Excludes(folder, backend.RoleNone); got != want {
			t.Errorf("Excludes(%q) = %v, want %v", folder, got, want)
		}
	}
	if err := (Filter{Folders: []string{"[unclosed"}}).Validate(); err == nil {
		t.Fatalf("a broken pattern was accepted")
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	for value, want := range map[string]string{
		"2025-01-31": "2025-01-31",
		"30d":        "2026-09-26",
		"2w":         "2026-10-12",
		"12m":        "2025-10-26",
		"2y":         "2024-10-26",
		"":           "0001-01-01",
	} {
		got, err := ParseSince(value, now)
		if err != nil || got.Format(time.DateOnly) != want {
			t.Errorf("ParseSince(%q) = %v, %v, want %s", value, got, err, want)
		}
	}
	for _, bad := range []string{"yesterday", "12", "0d", "2025-13-01", "-3d"} {
		if _, err := ParseSince(bad, now); err == nil {
			t.Errorf("ParseSince(%q) was accepted", bad)
		}
	}
}
