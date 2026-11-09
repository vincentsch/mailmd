package sync

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"time"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/mail"
	"github.com/vincentsch/mailmd/internal/writer"
)

// DefaultBatchSize is how many messages one fetch asks for.
const DefaultBatchSize = 100

// A failed provider call is tried at most maxAttempts times, with at most
// maxWait of waiting in total.
const (
	maxAttempts = 5
	maxWait     = 15 * time.Minute
)

// Options are the settings of one sync of one account.
type Options struct {
	Account accounts.AccountConfig
	// Root is the folder that holds the account's archive folder.
	Root   string
	DryRun bool
	Filter Filter
	// Since limits a first sync to newer mail. Zero means all mail.
	Since time.Time
	// KeepRaw saves each original message as an .eml file next to its
	// Markdown file.
	KeepRaw bool
	// PreferText uses the plain text part of a message instead of its
	// converted HTML part.
	PreferText                     bool
	MaxMessageBytes, MaxBatchBytes int64
	// BatchSize is how many messages one fetch returns. Zero means
	// DefaultBatchSize.
	BatchSize int
	// Open connects to the provider.
	Open func(context.Context) (backend.Backend, error)
	// Progress, when set, is called while folders are synced.
	Progress func(Progress)
	Clock    func() time.Time
	Sleep    func(context.Context, time.Duration) error
	Random   func() float64
	// Write replaces writer.WriteMessage in tests.
	Write func(context.Context, writer.Options, mail.Message) (writer.WriteResult, error)
}

// Progress reports how far a folder is.
type Progress struct {
	Account string
	Folder  string
	// Done counts the messages handled so far in this folder.
	Done int
	// Remaining is how many messages are left, when the provider knows.
	Remaining int
	// Finished is set on the last call for a folder.
	Finished bool
	Result   *FolderResult
}

// Result describes one sync of one account. Its JSON form is what
// mailmd sync --json prints, and scripts rely on it.
type Result struct {
	Account   string `json:"account"`
	Backend   string `json:"backend"`
	Archive   string `json:"archive"`
	DryRun    bool   `json:"dry_run"`
	Outcome   string `json:"outcome"`
	New       int    `json:"new"`
	Updated   int    `json:"updated"`
	Unchanged int    `json:"unchanged"`
	Excluded  int    `json:"excluded"`
	// Deleted counts messages deleted on the server. Their files stay.
	Deleted     int                              `json:"deleted_on_server"`
	Attachments int                              `json:"attachments"`
	Folders     []FolderResult                   `json:"folders"`
	Diagnostics map[backend.DiagnosticReason]int `json:"diagnostics"`
	Retries     int                              `json:"retries"`
	// NoReadMarks is set after the first sync of an archive when none of
	// its messages is marked as read or answered. Either they are all
	// unread, or the server doesn't keep these marks.
	NoReadMarks bool       `json:"no_read_marks"`
	Started     time.Time  `json:"started"`
	Seconds     float64    `json:"seconds"`
	Error       *ErrorInfo `json:"error"`
}

// FolderResult describes one folder of a sync.
type FolderResult struct {
	Path string `json:"path"`
	// Status is synced, excluded, missing or failed.
	Status      string                           `json:"status"`
	Reason      string                           `json:"reason,omitempty"`
	New         int                              `json:"new"`
	Updated     int                              `json:"updated"`
	Unchanged   int                              `json:"unchanged"`
	Deleted     int                              `json:"deleted_on_server"`
	Diagnostics map[backend.DiagnosticReason]int `json:"diagnostics"`
}

// Outcomes of a sync.
const (
	OutcomeCompleted    = "completed"
	OutcomeWarnings     = "completed_with_warnings"
	OutcomeFolderErrors = "completed_with_errors"
	OutcomeFailed       = "failed"
	OutcomeInterrupted  = "interrupted"
)

// RunError is returned when a sync stops early.
type RunError struct {
	Info  ErrorInfo
	Cause error
}

func (e *RunError) Error() string { return e.Info.Message }
func (e *RunError) Unwrap() error { return e.Cause }

// AllMailPath names the one scope that holds all labels of a Gmail account.
const AllMailPath = "All mail"

// job is one provider scope to fetch. Gmail shows all labels through one
// scope, so a job can stand for several folders.
type job struct {
	key  string
	path string
	// shared is set when one scope holds several folders, like the labels
	// of a Gmail account. A message's ref is then unique in the account.
	shared bool
	result int // index into Result.Folders
}

type run struct {
	opts    Options
	dir     string
	state   State
	saved   []byte
	result  Result
	conn    backend.Backend
	slept   time.Duration
	planner *writer.PlanningSession
	roles   map[string]backend.Role
	// index holds index.jsonl by path. indexed is set when lines were added,
	// so the file gets rewritten without repeats at the end.
	index   map[string]writer.IndexEntry
	indexed bool
	// firstFolder maps the refs of messages from a shared scope to the
	// folder their file is in. It is built when first needed.
	firstFolder map[string]string
	// filter is the folder filter for the folders of this account.
	filter Filter
	// dirs maps provider folders to their folders on disk. Two folders whose
	// names differ only in case get a folder each.
	dirs map[string]string
	// threads joins IMAP messages to threads the archive knows.
	threads *threads
	// keepIn names the file a refetched message replaces, by its ref.
	keepIn map[string]writer.IndexEntry
	// written holds the index entries of the last batch, by ref.
	written map[string]writer.IndexEntry
	// existed holds the refs of the last batch whose file was in the
	// archive before.
	existed map[string]bool
	// first is set when the archive had no sync state yet. marked is set
	// when a message is marked as read or answered.
	first, marked bool
}

// Run syncs one account. It returns the result also when it fails.
func Run(ctx context.Context, opts Options) (Result, error) {
	r := &run{opts: withDefaults(opts), roles: map[string]backend.Role{}}
	r.result = Result{
		Account:     opts.Account.Name,
		Backend:     opts.Account.Backend,
		DryRun:      opts.DryRun,
		Folders:     []FolderResult{},
		Diagnostics: map[backend.DiagnosticReason]int{},
		Started:     r.opts.Clock(),
	}
	err := r.run(ctx)
	r.closeConnection()
	r.result.Seconds = r.opts.Clock().Sub(r.result.Started).Seconds()
	if err != nil {
		info := Describe(err, opts.Account)
		r.result.Error = &info
		r.result.Outcome = OutcomeFailed
		if info.Category == CategoryCanceled {
			r.result.Outcome = OutcomeInterrupted
		}
		return r.result, &RunError{Info: info, Cause: err}
	}
	r.result.Outcome = r.outcome()
	r.result.NoReadMarks = r.first && r.result.New > 0 && !r.marked
	return r.result, nil
}

func withDefaults(opts Options) Options {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	if opts.Random == nil {
		opts.Random = rand.Float64
	}
	if opts.Write == nil {
		opts.Write = writer.WriteMessage
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	return opts
}

func (r *run) run(ctx context.Context) error {
	request, err := (backend.FetchRequest{Limit: r.opts.BatchSize, MaxMessageBytes: r.opts.MaxMessageBytes, MaxBatchBytes: r.opts.MaxBatchBytes}).WithDefaults()
	if err != nil {
		return err
	}
	if r.dir, err = accountDir(r.opts.Root, r.opts.Account); err != nil {
		return err
	}
	r.result.Archive = shownDir(r.opts.Root, r.opts.Account)
	if r.opts.DryRun {
		r.planner = writer.NewPlanningSession()
	} else {
		unlock, err := lockArchive(r.dir)
		if err != nil {
			return err
		}
		defer unlock()
	}
	if r.state, r.saved, err = LoadState(r.dir, r.opts.Account.Backend); err != nil {
		return err
	}
	r.first = r.saved == nil
	if err := r.loadIndex(); err != nil {
		return err
	}
	folders, err := r.connect(ctx)
	if err != nil {
		return err
	}
	jobs, err := r.plan(folders)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if err := r.syncFolder(ctx, j, request); err != nil {
			return err
		}
	}
	r.closeConnection()
	if err := r.saveState(); err != nil {
		return err
	}
	if err := r.finishThreads(ctx); err != nil {
		return err
	}
	return r.finishIndex()
}

// connect opens the provider and lists its folders. It tries again after
// failures that can go away.
func (r *run) connect(ctx context.Context) ([]backend.Folder, error) {
	for attempt := 1; ; attempt++ {
		err := r.open(ctx)
		if err == nil {
			var folders []backend.Folder
			if folders, err = r.conn.Folders(ctx); err == nil {
				return folders, nil
			}
		}
		if err = r.wait(ctx, err, attempt); err != nil {
			return nil, err
		}
	}
}

func (r *run) open(ctx context.Context) error {
	r.closeConnection()
	b, err := r.opts.Open(ctx)
	if err != nil {
		_ = backend.Close(b)
		return err
	}
	if b == nil {
		return errors.New("sync: the provider returned no connection")
	}
	r.conn = b
	return nil
}

func (r *run) closeConnection() {
	if r.conn != nil {
		_ = r.conn.Close()
		r.conn = nil
	}
}

// wait decides whether a failed call is tried again. It returns nil after
// waiting, or the error that ends the sync.
func (r *run) wait(ctx context.Context, err error, attempt int) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !errors.Is(err, backend.ErrRetryable) && !errors.Is(err, backend.ErrClosed) {
		return err
	}
	if attempt >= maxAttempts {
		return errors.Join(ErrRetryExhausted, err)
	}
	var delay time.Duration
	var hint *backend.RetryableError
	if errors.As(err, &hint) && hint.RetryAfter > 0 {
		delay = hint.RetryAfter
	} else {
		// Wait a random time below 1 second, then below 2, 4 and 8 seconds.
		delay = time.Duration(min(max(r.opts.Random(), 0), 0.999) * float64(time.Second<<(attempt-1)))
	}
	if r.slept+delay > maxWait {
		return errors.Join(ErrRetryExhausted, err)
	}
	r.closeConnection()
	if err := r.opts.Sleep(ctx, delay); err != nil {
		return err
	}
	r.slept += delay
	r.result.Retries++
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// plan matches the provider's folders with the saved state and decides which
// to sync. For a folder that disappeared, the files and the saved position
// stay.
func (r *run) plan(folders []backend.Folder) ([]job, error) {
	filter := r.opts.Filter.ForFolders(folders)
	if err := filter.CheckMatches(folders); err != nil {
		return nil, err
	}
	r.filter = filter
	var paths []string
	for _, f := range folders {
		paths = append(paths, f.Path)
	}
	r.dirs = writer.FolderDirs(paths)
	byKey := map[string][]backend.Folder{}
	var keys []string
	for _, f := range folders {
		r.roles[f.Path] = f.Role
		if !f.Fetchable || f.ScopeKey == "" {
			continue
		}
		if _, ok := byKey[f.ScopeKey]; !ok {
			keys = append(keys, f.ScopeKey)
		}
		byKey[f.ScopeKey] = append(byKey[f.ScopeKey], f)
	}
	wasMissing := map[string]bool{}
	for i := range r.state.Folders {
		wasMissing[r.state.Folders[i].Key] = r.state.Folders[i].Missing
		r.state.Folders[i].Missing = true
	}
	var jobs []job
	for _, key := range keys {
		group := byKey[key]
		path := group[0].Path
		if len(group) > 1 {
			path = AllMailPath
		}
		fs := r.state.folder(key)
		if fs == nil {
			r.state.Folders = append(r.state.Folders, FolderState{Key: key})
			fs = &r.state.Folders[len(r.state.Folders)-1]
		}
		fs.Path, fs.Missing = path, false
		excluded, reason := true, ""
		for _, f := range group {
			var out bool
			if out, reason = filter.Excludes(f.Path, f.Role); !out {
				excluded = false
				break
			}
		}
		result := FolderResult{Path: path, Status: "synced", Diagnostics: map[backend.DiagnosticReason]int{}}
		if excluded {
			result.Status, result.Reason = "excluded", reason
			r.result.Folders = append(r.result.Folders, result)
			continue
		}
		shared := len(group) > 1
		if shared {
			// One position covers all folders here, and the filter decides
			// for each message. The position has moved past the messages that
			// the filter left out, so a new filter starts the folder over.
			if fp := filter.fingerprint(); fs.Filter != fp {
				fs.Cursor, fs.Filter = "", fp
			}
		}
		r.applySince(fs)
		r.result.Folders = append(r.result.Folders, result)
		jobs = append(jobs, job{key: key, path: path, shared: shared, result: len(r.result.Folders) - 1})
	}
	for _, fs := range r.state.Folders {
		// A folder that went missing is reported once, by the sync that
		// noticed it. After that, mailmd status lists it.
		if fs.Missing && !wasMissing[fs.Key] {
			r.result.Folders = append(r.result.Folders, FolderResult{Path: fs.Path, Status: "missing", Reason: "no longer on the server; its files are kept", Diagnostics: map[backend.DiagnosticReason]int{}})
		}
	}
	r.state.sortFolders()
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].path < jobs[j].path })
	return jobs, nil
}

// applySince starts a folder over when the requested --since reaches further
// back than the mail fetched so far. Messages that are already archived are
// fetched again but not written again.
func (r *run) applySince(fs *FolderState) {
	want := ""
	if !r.opts.Since.IsZero() {
		want = r.opts.Since.UTC().Format(time.DateOnly)
	}
	if fs.Cursor == "" {
		fs.Since = want
		return
	}
	if fs.Since != "" && (want == "" || want < fs.Since) {
		fs.Cursor, fs.Since = "", want
	}
}

// syncFolder fetches one folder batch by batch. A problem that concerns only
// this folder is recorded in its result, and the sync goes on with the next
// folder. Other errors end the sync.
func (r *run) syncFolder(ctx context.Context, j job, request backend.FetchRequest) error {
	fr := &r.result.Folders[j.result]
	fs := r.state.folder(j.key)
	request.ScopeKey, request.FolderPath = j.key, j.path
	request.SkipJunk = r.filter.SkipsJunk(r.roles)
	if fs.Since != "" {
		request.Since, _ = time.Parse(time.DateOnly, fs.Since)
	}
	// A folder fetched from the start has fresh flags. Checking them again
	// would only cost time.
	fresh := fs.Cursor == ""
	done := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cursor, err := base64.StdEncoding.DecodeString(fs.Cursor)
		if err != nil {
			return ErrInvalidState
		}
		request.Cursor = cursor
		batch, err := r.fetch(ctx, request)
		switch {
		case errors.Is(err, backend.ErrScopeNotFound):
			fs.Missing = true
			fr.Status, fr.Reason = "missing", "no longer on the server; its files are kept"
			r.report(j, done, 0, true)
			return r.saveState()
		case err != nil && folderOnly(err):
			fr.Status, fr.Reason = "failed", Describe(err, r.opts.Account).Message
			r.closeConnection()
			r.report(j, done, 0, true)
			return nil
		case err != nil:
			return err
		}
		if !batch.Done && bytes.Equal(batch.Cursor, cursor) {
			fr.Status, fr.Reason = "failed", "the provider returned the same position twice"
			r.report(j, done, 0, true)
			return nil
		}
		if err := r.write(ctx, j, fr, batch.Messages); err != nil {
			return err
		}
		fs.Cursor = base64.StdEncoding.EncodeToString(batch.Cursor)
		problems, err := r.removed(ctx, j, fr, batch.Diagnostics)
		if err != nil {
			return err
		}
		for _, d := range r.state.addDiagnostics(j.path, problems, r.opts.Clock(), r.written) {
			// A folder that is read again, for example after a wider
			// --since, brings back the problems of messages that are
			// already saved. They were reported the first time.
			if r.existed[d.Ref] {
				continue
			}
			fr.Diagnostics[d.Reason]++
			r.result.Diagnostics[d.Reason]++
		}
		if err := r.saveState(); err != nil {
			return err
		}
		done += handled(batch)
		if batch.Done {
			if !fresh {
				if err := r.refresh(ctx, j, fs, fr); err != nil {
					return err
				}
			}
			if err := r.refetchStubs(ctx, j, fr, request); err != nil {
				return err
			}
			r.report(j, done, 0, true)
			return nil
		}
		r.report(j, done, batch.Remaining, false)
	}
}

func (r *run) report(j job, done, remaining int, finished bool) {
	if r.opts.Progress == nil {
		return
	}
	p := Progress{Account: r.opts.Account.Name, Folder: j.path, Done: done, Remaining: remaining, Finished: finished}
	if finished {
		fr := r.result.Folders[j.result]
		p.Result = &fr
	}
	r.opts.Progress(p)
}

// fetch gets one batch, reconnecting and retrying when that may help.
func (r *run) fetch(ctx context.Context, request backend.FetchRequest) (backend.FetchResult, error) {
	batch, err := r.call(ctx, func(b backend.Backend) (backend.FetchResult, error) { return b.Fetch(ctx, request) })
	if err != nil {
		return backend.FetchResult{}, err
	}
	if batch.RawBytes < 0 || batch.RawBytes > request.MaxBatchBytes || handled(batch) > request.Limit {
		return backend.FetchResult{}, errors.New("sync: the provider returned more than was asked for")
	}
	return batch, nil
}

// call runs one provider call, reconnecting and retrying when that may help.
func (r *run) call(ctx context.Context, do func(backend.Backend) (backend.FetchResult, error)) (backend.FetchResult, error) {
	for attempt := 1; ; attempt++ {
		var err error
		if r.conn == nil {
			err = r.open(ctx)
		}
		if err == nil {
			var batch backend.FetchResult
			if batch, err = do(r.conn); err == nil {
				return batch, nil
			}
		}
		if err = r.wait(ctx, err, attempt); err != nil {
			return backend.FetchResult{}, err
		}
	}
}

// handled counts the provider messages a batch covers. A message can come
// with a diagnostic, like one saved with its headers only, and counts once.
func handled(batch backend.FetchResult) int {
	refs := map[string]bool{}
	n := 0
	count := func(ref string) {
		if ref == "" || !refs[ref] {
			n++
		}
		refs[ref] = true
	}
	for _, m := range batch.Messages {
		count(m.Ref)
	}
	for _, d := range batch.Diagnostics {
		count(d.Ref)
	}
	return n
}

// folderOnly reports errors that concern one folder, after which the other
// folders can still be synced.
func folderOnly(err error) bool {
	switch Category(err) {
	case CategoryPermission, CategoryProtocol, CategoryInvalidCursor:
		return true
	}
	return false
}

// write saves one batch of messages, then flushes the files to disk.
func (r *run) write(ctx context.Context, j job, fr *FolderResult, messages []mail.Message) error {
	batch := writer.NewBatch()
	var entries []writer.IndexEntry
	r.written, r.existed = map[string]writer.IndexEntry{}, map[string]bool{}
	for _, m := range messages {
		if stub, ok := r.keepIn[m.Ref]; ok && m.Ref != "" {
			// A message fetched again, because it was too large before,
			// replaces the file of its stub.
			if m.LocalID, ok = writer.LocalIDOf(filepath.Join(r.dir, filepath.FromSlash(stub.Path))); !ok {
				continue
			}
			m.Folders = []string{stub.Folder}
		} else if !r.keep(j, m) {
			r.result.Excluded++
			continue
		}
		if j.shared {
			r.stayInPlace(&m)
		}
		r.marked = r.marked || m.Flags.Seen || m.Flags.Answered
		if r.opts.Account.Backend == "imap" {
			r.joinThread(&m)
		}
		opts := writer.Options{Root: filepath.Dir(r.dir), Account: r.opts.Account.ArchiveDir, KeepRaw: r.opts.KeepRaw, PreferText: r.opts.PreferText, FolderDirs: r.dirs, Batch: batch}
		var result writer.WriteResult
		var err error
		if r.planner != nil {
			result, err = r.planner.PlanMessage(ctx, opts, m)
		} else {
			result, err = r.opts.Write(ctx, opts, m)
		}
		if err != nil {
			return err
		}
		r.count(fr, result)
		added := writer.IndexEntries(m, result)
		if r.threads != nil {
			r.threads.written(m, added)
		}
		if len(added) > 0 && m.Ref != "" {
			r.written[m.Ref] = added[0]
		}
		if len(result.Files) > 0 && result.Files[0].Status != writer.WriteCreated && m.Ref != "" {
			r.existed[m.Ref] = true
		}
		entries = append(entries, added...)
	}
	if r.planner != nil {
		return nil
	}
	if err := r.addToIndex(entries, batch); err != nil {
		return err
	}
	return batch.Sync()
}

// stayInPlace puts a message that is already in the archive into the folder
// of its file. When a Gmail message gets other labels, for example because
// you archived it, its file is updated where it is. No second copy appears
// in another folder.
func (r *run) stayInPlace(m *mail.Message) {
	if m.Ref == "" {
		return
	}
	if r.firstFolder == nil {
		r.firstFolder = map[string]string{}
		paths := make([]string, 0, len(r.index))
		for path := range r.index {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if e := r.index[path]; e.Ref != "" && r.firstFolder[e.Ref] == "" {
				r.firstFolder[e.Ref] = e.Folder
			}
		}
	}
	if folder, ok := r.firstFolder[m.Ref]; ok {
		m.Folders = []string{folder}
		return
	}
	if len(m.Folders) > 0 {
		r.firstFolder[m.Ref] = m.Folders[0]
	}
}

// keep applies the folder filter to one message, when its folder alone
// doesn't decide. That is the case for a message with labels or with several
// folders, and for any message of a shared scope, like a Gmail message
// without labels in Archive. Other messages were filtered with their folder
// already.
func (r *run) keep(j job, m mail.Message) bool {
	switch {
	case len(m.Labels) > 0:
		return r.filter.KeepMessage(m.Labels, r.roles)
	case j.shared || len(m.Folders) > 1:
		return r.filter.KeepMessage(m.Folders, r.roles)
	}
	return true
}

func (r *run) count(fr *FolderResult, result writer.WriteResult) {
	if len(result.Files) == 0 {
		return
	}
	switch result.Files[0].Status {
	case writer.WriteCreated:
		fr.New++
		r.result.New++
	case writer.WriteUpdated:
		fr.Updated++
		r.result.Updated++
	default:
		fr.Unchanged++
		r.result.Unchanged++
	}
	for _, f := range result.Files {
		for _, a := range f.Attachments {
			if a.Status == writer.WriteCreated || a.Status == writer.WriteUpdated {
				r.result.Attachments++
			}
		}
	}
}

func (r *run) saveState() error {
	if r.opts.DryRun {
		return nil
	}
	saved, err := saveState(r.dir, r.state, r.saved)
	if err == nil {
		r.saved = saved
	}
	return err
}

func (r *run) outcome() string {
	outcome := OutcomeCompleted
	for _, f := range r.result.Folders {
		switch {
		case f.Status == "failed":
			return OutcomeFolderErrors
		case f.Status == "missing" || len(f.Diagnostics) > 0:
			outcome = OutcomeWarnings
		}
	}
	return outcome
}
