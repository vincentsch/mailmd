package imap

import (
	"context"

	"github.com/emersion/go-imap/v2"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/mail"
)

// Refresh reports the current flags of the messages up to the cursor. One
// FETCH of only flags covers the whole folder. When the server supports
// CONDSTORE and nothing changed since the last refresh, it sends nothing at
// all.
func (b *Backend) Refresh(ctx context.Context, req backend.RefreshRequest) (backend.RefreshResult, error) {
	mailbox, delimiter, present, err := decodeScopeKey(req.ScopeKey)
	if err != nil {
		return backend.RefreshResult{}, err
	}
	if !present {
		delimiter = ""
	}
	folder := displayPath(mailbox, delimiter)
	pos, err := decodeCursor(req.ScopeKey, req.Cursor)
	if err != nil {
		return backend.RefreshResult{}, err
	}
	unlock, err := b.begin(ctx)
	if err != nil {
		return backend.RefreshResult{}, err
	}
	defer unlock()

	b.conn.allow(1 << 20)
	condstore := b.client.Caps().Has(imap.CapCondStore)
	selected, err := b.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true, CondStore: condstore}).Wait()
	if err != nil {
		return backend.RefreshResult{}, b.selectError(ctx, folder, err)
	}
	if selected.UIDValidity != pos.validity || pos.last == 0 {
		// Fetch starts a renumbered folder over, so there is nothing to
		// check.
		return backend.RefreshResult{Unchanged: true}, nil
	}
	if condstore && selected.HighestModSeq != 0 && selected.HighestModSeq == pos.modseq &&
		selected.NumMessages == pos.exists && uint32(selected.UIDNext) == pos.uidNext {
		return backend.RefreshResult{Unchanged: true}, nil
	}

	b.conn.allow(1<<20 + int64(selected.NumMessages)<<10)
	set := imap.UIDSet{imap.UIDRange{Start: 1, Stop: imap.UID(pos.last)}}
	messages, err := b.client.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		return backend.RefreshResult{}, b.commandError(ctx, "check the flags in "+folder, err)
	}
	flags := make(map[string]mail.Flags, len(messages))
	for _, m := range messages {
		if m.UID != 0 && uint32(m.UID) <= pos.last {
			flags[formatRef(pos.validity, m.UID)] = normalizeFlags(m.Flags)
		}
	}
	pos.modseq, pos.exists, pos.uidNext = selected.HighestModSeq, selected.NumMessages, uint32(selected.UIDNext)
	return backend.RefreshResult{Flags: flags, Cursor: encodeCursor(req.ScopeKey, pos)}, nil
}
