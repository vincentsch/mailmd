package sync

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/writer"
)

// threads joins IMAP messages to the threads the archive already knows.
// IMAP has no thread IDs, so a message's thread is the first message of its
// References header. Many mail programs cut References short or send only
// In-Reply-To, so when a message's parent is in the archive, the message
// joins the parent's thread instead.
type threads struct {
	// byID maps a Message-ID to its thread.
	byID map[string]string
	// members are the messages of this sync, in the order they were
	// written. Their threads are checked again at the end, when all of
	// them are known.
	members []threadMember
}

type threadMember struct {
	id      string
	parents []string
	thread  string
	paths   []string
}

func newThreads(index map[string]writer.IndexEntry) *threads {
	t := &threads{byID: map[string]string{}}
	for _, e := range index {
		t.learn(e.MessageID, e.Thread)
	}
	return t
}

func (t *threads) learn(messageID, thread string) {
	if id := normalizeID(messageID); id != "" && thread != "" {
		t.byID[id] = thread
	}
}

// parents lists the messages a message replies to: its References, then
// In-Reply-To.
func parents(m mail.Message) []string {
	out := append([]string(nil), m.References...)
	return append(out, strings.Fields(m.InReplyTo)...)
}

// known returns the thread of the first parent the archive knows.
func (t *threads) known(id string, parents []string) string {
	for _, p := range parents {
		if p := normalizeID(p); p != "" && p != id {
			if thread := t.byID[p]; thread != "" {
				return thread
			}
		}
	}
	return ""
}

// joinThread sets the thread of an IMAP message before it is written. When
// none of its parents is known, the message stays in the thread the parser
// gave it. That is the first message of References, or else its own
// Message-ID.
// Subjects are never used, because a subject like "Invoice" would put
// unrelated mail into one thread.
func (r *run) joinThread(m *mail.Message) {
	if r.threads == nil {
		r.threads = newThreads(r.index)
	}
	if thread := r.threads.known(normalizeID(m.MessageID), parents(*m)); thread != "" {
		m.ThreadID = thread
	}
}

// written records a message that was just written.
func (t *threads) written(m mail.Message, entries []writer.IndexEntry) {
	t.learn(m.MessageID, m.ThreadID)
	member := threadMember{id: normalizeID(m.MessageID), parents: parents(m), thread: m.ThreadID}
	for _, e := range entries {
		member.paths = append(member.paths, e.Path)
	}
	t.members = append(t.members, member)
}

// finishThreads joins the messages of this sync whose parent came later in
// the same sync, for example a reply in Sent that was synced before the
// message in INBOX it answers. Their files get the new thread_id.
func (r *run) finishThreads(ctx context.Context) error {
	t := r.threads
	if t == nil || len(t.members) == 0 {
		return nil
	}
	batch := writer.NewBatch()
	var changed []writer.IndexEntry
	// A parent can itself move to another thread in the same pass, so a
	// chain of replies needs a few passes.
	for pass := 0; pass < 3; pass++ {
		moved := false
		for i := range t.members {
			member := &t.members[i]
			thread := t.known(member.id, member.parents)
			if thread == "" || thread == member.thread {
				continue
			}
			member.thread, moved = thread, true
			if member.id != "" {
				t.byID[member.id] = thread
			}
			for _, path := range member.paths {
				e, ok := r.index[path]
				if !ok {
					continue
				}
				if !r.opts.DryRun {
					if err := keptByYou(writer.SetThread(ctx, filepath.Join(r.dir, filepath.FromSlash(path)), thread, batch)); err != nil {
						return err
					}
				}
				e.Thread = thread
				changed = append(changed, e)
			}
		}
		if !moved {
			break
		}
	}
	if err := r.addToIndex(changed, batch); err != nil {
		return err
	}
	return batch.Sync()
}

// normalizeID makes Message-IDs comparable: no angle brackets, lower case.
func normalizeID(id string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(id), "<>"))
}
