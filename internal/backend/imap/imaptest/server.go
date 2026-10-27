package imaptest

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// TLSKind says how the test server encrypts the connection.
type TLSKind string

const (
	TLSImplicit TLSKind = "implicit"
	TLSStartTLS TLSKind = "starttls"
	TLSNone     TLSKind = "none"
)

// Options sets up a test server.
type Options struct {
	TLS TLSKind
	// BlockList and BlockFetch make LIST and FETCH wait until the test calls
	// UnblockList or UnblockFetch, or the server closes.
	BlockList  bool
	BlockFetch bool
	// ListError makes LIST fail with this response.
	ListError *imap.Error
	// Folders replaces the default mail when it is not nil.
	Folders []FolderSeed
}

// Server is a running in-process IMAP test server.
type Server struct {
	srv      *imapserver.Server
	listener net.Listener
	addr     string
	caPool   *x509.CertPool
	opts     Options

	username string
	password string

	mu      sync.Mutex
	folders map[string]*folder
	order   []string

	unblockList      chan struct{}
	unblockListOnce  sync.Once
	unblockFetch     chan struct{}
	unblockFetchOnce sync.Once
	closed           chan struct{}
	closeOnce        sync.Once

	debug lockedBuffer
}

// folder is the current state of one mailbox of the test server.
type folder struct {
	seed     FolderSeed
	validity uint32
	messages []MessageSeed
}

// Start starts a test server on 127.0.0.1 with the seeded mail.
func Start(t *testing.T, opts Options) *Server {
	t.Helper()
	if opts.TLS == "" {
		opts.TLS = TLSImplicit
	}
	cert, caPool, err := newTestCert()
	if err != nil {
		t.Fatalf("new test certificate: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &Server{
		listener:     l,
		addr:         l.Addr().String(),
		caPool:       caPool,
		opts:         opts,
		username:     "mailmd-user",
		password:     "mailmd-password",
		folders:      map[string]*folder{},
		unblockList:  make(chan struct{}),
		unblockFetch: make(chan struct{}),
		closed:       make(chan struct{}),
	}
	seeds := opts.Folders
	if seeds == nil {
		seeds = SeedFolders()
	}
	for _, seed := range seeds {
		seed = cloneSeed(seed)
		validity := seed.UIDValidity
		if validity == 0 {
			validity = 1
		}
		name := canonical(seed.Name)
		s.folders[name] = &folder{seed: seed, validity: validity, messages: cloneMessages(seed.Messages)}
		s.order = append(s.order, name)
	}

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	options := &imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &session{server: s}, &imapserver.GreetingData{}, nil
		},
		Caps:        imap.CapSet{imap.CapIMAP4rev1: {}},
		Logger:      discardLogger{},
		DebugWriter: &s.debug,
	}
	serveListener := l
	switch opts.TLS {
	case TLSImplicit:
		serveListener = tls.NewListener(l, tlsConfig)
		options.InsecureAuth = true
	case TLSStartTLS:
		options.TLSConfig = tlsConfig
	case TLSNone:
		options.InsecureAuth = true
	default:
		t.Fatalf("unknown TLS kind %q", opts.TLS)
	}
	s.srv = imapserver.New(options)
	go func() { _ = s.srv.Serve(serveListener) }()
	t.Cleanup(s.Close)
	return s
}

// Close stops the server and releases blocked commands.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		if s.srv != nil {
			_ = s.srv.Close()
		}
		_ = s.listener.Close()
		close(s.closed)
		s.UnblockList()
		s.UnblockFetch()
	})
}

func (s *Server) Addr() string { return s.addr }

func (s *Server) Host() string {
	host, _, err := net.SplitHostPort(s.addr)
	if err != nil {
		return "127.0.0.1"
	}
	return host
}

func (s *Server) Port() int {
	_, port, _ := net.SplitHostPort(s.addr)
	n, _ := strconv.Atoi(port)
	return n
}

func (s *Server) CertPool() *x509.CertPool { return s.caPool.Clone() }
func (s *Server) Username() string         { return s.username }
func (s *Server) Password() string         { return s.password }

func (s *Server) UnblockList()  { s.unblockListOnce.Do(func() { close(s.unblockList) }) }
func (s *Server) UnblockFetch() { s.unblockFetchOnce.Do(func() { close(s.unblockFetch) }) }

// ScopeKey returns the scope key the IMAP backend uses for mailbox.
func (s *Server) ScopeKey(mailbox string) string {
	s.mu.Lock()
	f, ok := s.folders[canonical(mailbox)]
	s.mu.Unlock()
	if ok {
		return expectedScopeKey(f.seed.Name, f.seed.Delimiter, f.seed.Delimiter != "")
	}
	return expectedScopeKey(mailbox, "/", true)
}

// SeededMessages returns the expected metadata of the messages in mailbox.
func (s *Server) SeededMessages(mailbox string) []ExpectedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.folders[canonical(mailbox)]
	if f == nil {
		return nil
	}
	out := make([]ExpectedMessage, 0, len(f.messages))
	for _, msg := range f.messages {
		expected := msg.Expected
		expected.UID = msg.UID
		expected.Flags = append([]string(nil), msg.Flags...)
		out = append(out, expected)
	}
	return out
}

// BumpUIDValidity simulates a server that renumbers a mailbox.
func (s *Server) BumpUIDValidity(mailbox string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.folders[canonical(mailbox)]; f != nil {
		f.validity++
	}
}

// AppendMessage adds a message with the UID given in seed.
func (s *Server) AppendMessage(mailbox string, seed MessageSeed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.folders[canonical(mailbox)]
	if f == nil {
		return
	}
	f.messages = append(f.messages, cloneMessage(seed))
	sort.Slice(f.messages, func(i, j int) bool { return f.messages[i].UID < f.messages[j].UID })
}

// Expunge removes a message, as if another client had deleted it.
func (s *Server) Expunge(mailbox string, uid uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.folders[canonical(mailbox)]
	if f == nil {
		return
	}
	kept := f.messages[:0]
	for _, msg := range f.messages {
		if msg.UID != uid {
			kept = append(kept, msg)
		}
	}
	f.messages = kept
}

// SetFlags replaces the flags of a message, as if another client had
// changed them.
func (s *Server) SetFlags(mailbox string, uid uint32, flags ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.folders[canonical(mailbox)]
	if f == nil {
		return
	}
	for i := range f.messages {
		if f.messages[i].UID == uid {
			f.messages[i].Flags = append([]string(nil), flags...)
		}
	}
}

// DebugLog returns the raw protocol traffic, both directions.
func (s *Server) DebugLog() string { return s.debug.String() }

// session serves one connection from the seeded folders.
type session struct {
	server   *Server
	selected string
}

var errUnsupported = &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Not supported by the test server"}

func (sess *session) Close() error { return nil }

func (sess *session) Login(username, password string) error {
	if username != sess.server.username || password != sess.server.password {
		return imapserver.ErrAuthFailed
	}
	return nil
}

func (sess *session) Select(name string, options *imap.SelectOptions) (*imap.SelectData, error) {
	s := sess.server
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.folders[canonical(name)]
	if f == nil {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNonExistent, Text: "No such mailbox"}
	}
	if f.seed.SelectError != nil {
		err := *f.seed.SelectError
		return nil, &err
	}
	if hasAttr(f.seed.Attributes, string(imap.MailboxAttrNoSelect)) {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Mailbox is not selectable"}
	}
	sess.selected = canonical(name)
	data := &imap.SelectData{
		Flags:          []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDraft, imap.FlagDeleted},
		PermanentFlags: []imap.Flag{imap.FlagWildcard},
		NumMessages:    uint32(len(f.messages)),
		UIDValidity:    f.validity,
	}
	if !f.seed.OmitUIDNext {
		data.UIDNext = imap.UID(uidNext(f.messages))
	}
	if f.seed.OmitUIDValidity {
		data.UIDValidity = 0
	}
	return data, nil
}

func (sess *session) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	s := sess.server
	if s.opts.ListError != nil {
		err := *s.opts.ListError
		return &err
	}
	if s.opts.BlockList {
		select {
		case <-s.unblockList:
		case <-s.closed:
			return errUnsupported
		}
	}
	s.mu.Lock()
	var list []*imap.ListData
	for _, name := range s.order {
		f := s.folders[name]
		data := &imap.ListData{Mailbox: f.seed.Name}
		if f.seed.Delimiter != "" {
			data.Delim = []rune(f.seed.Delimiter)[0]
		}
		for _, attr := range f.seed.Attributes {
			data.Attrs = append(data.Attrs, imap.MailboxAttr(attr))
		}
		list = append(list, data)
	}
	s.mu.Unlock()
	for _, data := range list {
		if err := w.WriteList(data); err != nil {
			return err
		}
	}
	return nil
}

func (sess *session) Unselect() error {
	sess.selected = ""
	return nil
}

// messages returns a copy of the selected folder's messages in UID order.
func (sess *session) messages() []MessageSeed {
	s := sess.server
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.folders[sess.selected]
	if f == nil {
		return nil
	}
	return cloneMessages(f.messages)
}

func (sess *session) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	messages := sess.messages()
	var max imap.UID
	if len(messages) > 0 {
		max = imap.UID(messages[len(messages)-1].UID)
	}
	var uids imap.UIDSet
	var seqs imap.SeqSet
	for i, msg := range messages {
		if !matches(msg, uint32(i+1), max, criteria) {
			continue
		}
		uids.AddNum(imap.UID(msg.UID))
		seqs.AddNum(uint32(i + 1))
	}
	if kind == imapserver.NumKindUID {
		return &imap.SearchData{All: uids}, nil
	}
	return &imap.SearchData{All: seqs}, nil
}

// matches supports the search keys the IMAP backend uses: UID sets and
// SINCE.
func matches(msg MessageSeed, seq uint32, max imap.UID, criteria *imap.SearchCriteria) bool {
	for _, set := range criteria.UID {
		if !resolve(set, max).Contains(imap.UID(msg.UID)) {
			return false
		}
	}
	for _, set := range criteria.SeqNum {
		if !set.Contains(seq) {
			return false
		}
	}
	if !criteria.Since.IsZero() {
		y, m, d := criteria.Since.Date()
		if msg.InternalDate.Before(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)) {
			return false
		}
	}
	return true
}

// resolve replaces "*" with the highest UID, as RFC 3501 requires. That also
// makes "n:*" match the highest UID when it is below n.
func resolve(set imap.UIDSet, max imap.UID) imap.UIDSet {
	out := make(imap.UIDSet, len(set))
	for i, r := range set {
		if r.Start == 0 {
			r.Start = max
		}
		if r.Stop == 0 {
			r.Stop = max
		}
		if r.Start > r.Stop {
			r.Start, r.Stop = r.Stop, r.Start
		}
		out[i] = r
	}
	return out
}

func (sess *session) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	s := sess.server
	if s.opts.BlockFetch {
		select {
		case <-s.unblockFetch:
		case <-s.closed:
			return errUnsupported
		}
	}
	messages := sess.messages()
	var max imap.UID
	if len(messages) > 0 {
		max = imap.UID(messages[len(messages)-1].UID)
	}
	for i, msg := range messages {
		seq := uint32(i + 1)
		switch set := numSet.(type) {
		case imap.UIDSet:
			if !resolve(set, max).Contains(imap.UID(msg.UID)) {
				continue
			}
		case imap.SeqSet:
			if !set.Contains(seq) {
				continue
			}
		}
		if err := writeMessage(w.CreateMessage(seq), msg, options); err != nil {
			return err
		}
	}
	return nil
}

func writeMessage(w *imapserver.FetchResponseWriter, msg MessageSeed, options *imap.FetchOptions) error {
	w.WriteUID(imap.UID(msg.UID))
	if options.Flags {
		flags := make([]imap.Flag, 0, len(msg.Flags))
		for _, flag := range msg.Flags {
			flags = append(flags, imap.Flag(flag))
		}
		w.WriteFlags(flags)
	}
	if options.InternalDate {
		date := msg.InternalDate
		if date.IsZero() {
			date = time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
		}
		w.WriteInternalDate(date)
	}
	if options.RFC822Size && !msg.OmitSize {
		size := int64(len(msg.Raw))
		if msg.ReportedSize != nil {
			size = int64(*msg.ReportedSize)
		}
		w.WriteRFC822Size(size)
	}
	for _, section := range options.BodySection {
		if msg.OmitBody {
			continue
		}
		data := msg.Raw
		if section.Specifier != imap.PartSpecifierNone || len(section.Part) > 0 || section.Partial != nil {
			data = imapserver.ExtractBodySection(bytes.NewReader(msg.Raw), section)
		}
		body := w.WriteBodySection(section, int64(len(data)))
		_, writeErr := body.Write(data)
		if err := body.Close(); err != nil {
			return err
		}
		if writeErr != nil {
			return writeErr
		}
	}
	return w.Close()
}

func (sess *session) Create(string, *imap.CreateOptions) error { return errUnsupported }
func (sess *session) Delete(string) error                      { return errUnsupported }
func (sess *session) Rename(string, string, *imap.RenameOptions) error {
	return errUnsupported
}
func (sess *session) Subscribe(string) error   { return errUnsupported }
func (sess *session) Unsubscribe(string) error { return errUnsupported }
func (sess *session) Status(string, *imap.StatusOptions) (*imap.StatusData, error) {
	return nil, errUnsupported
}
func (sess *session) Append(string, imap.LiteralReader, *imap.AppendOptions) (*imap.AppendData, error) {
	return nil, errUnsupported
}
func (sess *session) Poll(*imapserver.UpdateWriter, bool) error { return nil }
func (sess *session) Idle(*imapserver.UpdateWriter, <-chan struct{}) error {
	return errUnsupported
}
func (sess *session) Expunge(*imapserver.ExpungeWriter, *imap.UIDSet) error { return errUnsupported }
func (sess *session) Store(*imapserver.FetchWriter, imap.NumSet, *imap.StoreFlags, *imap.StoreOptions) error {
	return errUnsupported
}
func (sess *session) Copy(imap.NumSet, string) (*imap.CopyData, error) { return nil, errUnsupported }

func canonical(name string) string {
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	return name
}

func uidNext(messages []MessageSeed) uint32 {
	var max uint32
	for _, msg := range messages {
		if msg.UID > max {
			max = msg.UID
		}
	}
	return max + 1
}

func hasAttr(attrs []string, target string) bool {
	for _, attr := range attrs {
		if strings.EqualFold(attr, target) {
			return true
		}
	}
	return false
}

func cloneSeed(seed FolderSeed) FolderSeed {
	seed.Attributes = append([]string(nil), seed.Attributes...)
	seed.Messages = cloneMessages(seed.Messages)
	if seed.SelectError != nil {
		err := *seed.SelectError
		seed.SelectError = &err
	}
	return seed
}

func cloneMessages(messages []MessageSeed) []MessageSeed {
	out := make([]MessageSeed, len(messages))
	for i := range messages {
		out[i] = cloneMessage(messages[i])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out
}

func cloneMessage(message MessageSeed) MessageSeed {
	if message.ReportedSize != nil {
		size := *message.ReportedSize
		message.ReportedSize = &size
	}
	message.Flags = append([]string(nil), message.Flags...)
	message.Raw = append([]byte(nil), message.Raw...)
	message.Expected.Flags = append([]string(nil), message.Expected.Flags...)
	return message
}

// lockedBuffer collects the protocol traffic from server goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// discardLogger drops the server's log, which is only noise in test output.
type discardLogger struct{}

func (discardLogger) Printf(string, ...interface{}) {}
