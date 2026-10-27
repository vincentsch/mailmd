package gmail

import (
	"context"
	"strconv"

	gmailapi "google.golang.org/api/gmail/v1"
)

// gmailClient is the part of the Gmail API that the backend uses. Tests put a
// fake behind it, so they can test the backend without HTTP.
type gmailClient interface {
	Profile(ctx context.Context, userID string) (gmailProfile, error)
	Labels(ctx context.Context, userID string) ([]gmailLabel, error)
	ListMessages(ctx context.Context, userID, pageToken string, includeSpamTrash bool, query string, maxResults int64) (gmailMessagePage, error)
	GetMessage(ctx context.Context, userID, id string) (gmailMessage, error)
	ListHistory(ctx context.Context, userID, startHistoryID, pageToken string, maxResults int64) (gmailHistoryPage, error)
}

// gmailProfile has the fields of Users.GetProfile that the backend needs. The
// email address goes into the account fingerprint, and connect shows it.
type gmailProfile struct {
	EmailAddress string
	HistoryID    string
}

// gmailLabel has the label fields that decide folders and flags.
type gmailLabel struct {
	ID   string
	Name string
	Type string
}

// gmailMessageRef is a message as the list and history calls return it,
// before the backend fetches the raw message.
type gmailMessageRef struct {
	ID       string
	ThreadID string
}

// gmailMessagePage is one page of the message list, with the token for the
// next page.
type gmailMessagePage struct {
	Messages      []gmailMessageRef
	NextPageToken string
}

// gmailMessage is one fetched message. Raw is the RFC 822 source in Gmail's
// base64url encoding.
type gmailMessage struct {
	SizeEstimate int64
	ID           string
	ThreadID     string
	LabelIDs     []string
	Raw          string
	InternalDate int64
}

// gmailHistoryPage is one page of the history list, with the token for the
// next page.
type gmailHistoryPage struct {
	History       []gmailHistory
	NextPageToken string
}

// gmailHistory holds the kinds of change that the backend acts on. Gmail's
// History.Messages field lists every message of a change and is not used.
type gmailHistory struct {
	ID              string
	MessagesAdded   []gmailMessageRef
	LabelsAdded     []gmailMessageRef
	LabelsRemoved   []gmailMessageRef
	MessagesDeleted []gmailMessageRef
}

// officialClient is gmailClient on top of Google's Go library for the Gmail
// API.
type officialClient struct {
	svc *gmailapi.Service
}

func (c officialClient) Profile(ctx context.Context, userID string) (gmailProfile, error) {
	p, err := c.svc.Users.GetProfile(userID).Context(ctx).Do()
	if err != nil {
		return gmailProfile{}, err
	}
	return gmailProfile{
		EmailAddress: p.EmailAddress,
		HistoryID:    strconv.FormatUint(p.HistoryId, 10),
	}, nil
}

func (c officialClient) Labels(ctx context.Context, userID string) ([]gmailLabel, error) {
	resp, err := c.svc.Users.Labels.List(userID).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	out := make([]gmailLabel, 0, len(resp.Labels))
	for _, label := range resp.Labels {
		if label == nil {
			continue
		}
		out = append(out, gmailLabel{ID: label.Id, Name: label.Name, Type: label.Type})
	}
	return out, nil
}

func (c officialClient) ListMessages(ctx context.Context, userID, pageToken string, includeSpamTrash bool, query string, maxResults int64) (gmailMessagePage, error) {
	call := c.svc.Users.Messages.List(userID).IncludeSpamTrash(includeSpamTrash).MaxResults(maxResults).Context(ctx)
	if query != "" {
		call = call.Q(query)
	}
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}
	resp, err := call.Do()
	if err != nil {
		return gmailMessagePage{}, err
	}
	out := gmailMessagePage{NextPageToken: resp.NextPageToken}
	for _, msg := range resp.Messages {
		if msg == nil {
			continue
		}
		out.Messages = append(out.Messages, gmailMessageRef{ID: msg.Id, ThreadID: msg.ThreadId})
	}
	return out, nil
}

func (c officialClient) GetMessage(ctx context.Context, userID, id string) (gmailMessage, error) {
	msg, err := c.svc.Users.Messages.Get(userID, id).Format("raw").Context(ctx).Do()
	if err != nil {
		return gmailMessage{}, err
	}
	return gmailMessage{
		ID:           msg.Id,
		ThreadID:     msg.ThreadId,
		LabelIDs:     append([]string(nil), msg.LabelIds...),
		Raw:          msg.Raw,
		InternalDate: msg.InternalDate,
		SizeEstimate: msg.SizeEstimate,
	}, nil
}

// gmailHeader is one header line of a message.
type gmailHeader struct {
	Name, Value string
}

// MessageHeaders returns the headers and labels of a message without its
// body.
func (c officialClient) MessageHeaders(ctx context.Context, userID, id string) (gmailMessage, []gmailHeader, error) {
	msg, err := c.svc.Users.Messages.Get(userID, id).Format("metadata").Context(ctx).Do()
	if err != nil {
		return gmailMessage{}, nil, err
	}
	var headers []gmailHeader
	if msg.Payload != nil {
		for _, h := range msg.Payload.Headers {
			if h != nil {
				headers = append(headers, gmailHeader{Name: h.Name, Value: h.Value})
			}
		}
	}
	return gmailMessage{ID: msg.Id, ThreadID: msg.ThreadId, LabelIDs: msg.LabelIds, InternalDate: msg.InternalDate, SizeEstimate: msg.SizeEstimate}, headers, nil
}

func (c officialClient) MessageSize(ctx context.Context, userID, id string) (int64, error) {
	message, err := c.svc.Users.Messages.Get(userID, id).Format("metadata").Fields("sizeEstimate").Context(ctx).Do()
	if err != nil {
		return 0, err
	}
	return message.SizeEstimate, nil
}

func (c officialClient) ListHistory(ctx context.Context, userID, startHistoryID, pageToken string, maxResults int64) (gmailHistoryPage, error) {
	// The cursor has already checked the history ID. It is parsed here because
	// Google's library wants a uint64.
	start, err := strconv.ParseUint(startHistoryID, 10, 64)
	if err != nil {
		return gmailHistoryPage{}, invalidCursor("history id out of range")
	}
	call := c.svc.Users.History.List(userID).StartHistoryId(start).MaxResults(maxResults).Context(ctx)
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}
	resp, err := call.Do()
	if err != nil {
		return gmailHistoryPage{}, err
	}
	out := gmailHistoryPage{NextPageToken: resp.NextPageToken}
	for _, rec := range resp.History {
		if rec == nil {
			continue
		}
		h := gmailHistory{ID: strconv.FormatUint(rec.Id, 10)}
		for _, item := range rec.MessagesAdded {
			if item != nil {
				h.MessagesAdded = append(h.MessagesAdded, refFromAPIMessage(item.Message))
			}
		}
		for _, item := range rec.LabelsAdded {
			if item != nil {
				h.LabelsAdded = append(h.LabelsAdded, refFromAPIMessage(item.Message))
			}
		}
		for _, item := range rec.LabelsRemoved {
			if item != nil {
				h.LabelsRemoved = append(h.LabelsRemoved, refFromAPIMessage(item.Message))
			}
		}
		for _, item := range rec.MessagesDeleted {
			if item != nil {
				h.MessagesDeleted = append(h.MessagesDeleted, refFromAPIMessage(item.Message))
			}
		}
		out.History = append(out.History, h)
	}
	return out, nil
}

// refFromAPIMessage returns an empty ref for a nil message, which callers
// then leave out.
func refFromAPIMessage(msg *gmailapi.Message) gmailMessageRef {
	if msg == nil {
		return gmailMessageRef{}
	}
	return gmailMessageRef{ID: msg.Id, ThreadID: msg.ThreadId}
}
