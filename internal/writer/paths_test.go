package writer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vincentsch/mailmd/internal/mail"
)

func TestPlanMessagePathCases(t *testing.T) {
	root := t.TempDir()
	base := mail.Message{LocalID: plainLocalID, Subject: "", BodyText: "body"}
	tests := []struct {
		name    string
		msg     mail.Message
		wantRel []string
	}{
		{
			name: "empty folders date-less no subject",
			msg:  base,
			wantRel: []string{
				"Acct/Unfiled/no-date/no-date-no-subject--0123456789abcdef.md",
			},
		},
		{
			name: "empty raw folder",
			msg:  withFolders(base, ""),
			wantRel: []string{
				"Acct/Unfiled/no-date/no-date-no-subject--0123456789abcdef.md",
			},
		},
		{
			name: "folder normalizes to no segments",
			msg:  withFolders(base, "/"),
			wantRel: []string{
				"Acct/Unfiled/no-date/no-date-no-subject--0123456789abcdef.md",
			},
		},
		{
			name: "nested and unsafe folders",
			msg:  withFolders(base, "Inbox/../.git/CON", "Inbox/../.git/CON"),
			wantRel: []string{
				"Acct/Inbox/unnamed-5ec1f7e7/_git-4d0a4dff/_CON-a3dbc4b6/no-date/no-date-no-subject--0123456789abcdef.md",
			},
		},
		{
			name: "multi folder sorted",
			msg:  withFolders(base, "Z", "A/B"),
			wantRel: []string{
				"Acct/A/B/no-date/no-date-no-subject--0123456789abcdef.md",
				"Acct/Z/no-date/no-date-no-subject--0123456789abcdef.md",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := PlanMessage(context.Background(), Options{Root: root, Account: "Acct"}, tt.msg)
			if err != nil {
				t.Fatalf("PlanMessage: %v", err)
			}
			got := make([]string, 0, len(result.Files))
			for _, file := range result.Files {
				got = append(got, file.RelPath)
				if !filepath.IsAbs(file.Path) || filepath.Clean(file.Path) != file.Path {
					t.Fatalf("path is not absolute clean: %q", file.Path)
				}
				if strings.Contains(file.RelPath, string(os.PathSeparator)) && os.PathSeparator != '/' {
					t.Fatalf("RelPath is not slash-separated: %q", file.RelPath)
				}
			}
			if !reflect.DeepEqual(got, tt.wantRel) {
				t.Fatalf("RelPath = %#v, want %#v", got, tt.wantRel)
			}
		})
	}
}

func TestRawFoldersCollapseToOneDiskCopyAndRemainInFrontmatter(t *testing.T) {
	root := t.TempDir()
	msg := mail.Message{
		LocalID:  plainLocalID,
		Subject:  "Folders",
		Folders:  []string{"A//B", "A/B"},
		BodyText: "body",
	}
	result, err := WriteMessage(context.Background(), Options{Root: root, Account: "Acct"}, msg)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if got, want := len(result.Files), 1; got != want {
		t.Fatalf("files = %d, want %d", got, want)
	}
	if got, want := result.Files[0].RelPath, "Acct/A/B/no-date/no-date-folders--0123456789abcdef.md"; got != want {
		t.Fatalf("RelPath = %q, want %q", got, want)
	}
	content := readFile(t, result.Files[0].Path)
	for _, want := range [][]byte{
		[]byte("folders: [\"A//B\", \"A/B\"]\n"),
	} {
		if !bytes.Contains(content, want) {
			t.Fatalf("frontmatter missing %q:\n%s", want, content)
		}
	}
}
