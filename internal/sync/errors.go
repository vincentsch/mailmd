package sync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/vincentsch/mailmd/internal/accounts"
	"github.com/vincentsch/mailmd/internal/auth"
	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/imap"
	"github.com/vincentsch/mailmd/internal/privatefs"
	"github.com/vincentsch/mailmd/internal/writer"
)

// ErrRetryExhausted means a provider call failed on each try, and mailmd
// gave up.
var ErrRetryExhausted = errors.New("sync: gave up after several attempts")

// Error categories. They are part of the JSON output and decide the exit
// code.
const (
	CategoryCanceled      = "canceled"
	CategoryLocked        = "locked"
	CategoryAuth          = "login_failed"
	CategoryCredential    = "missing_credential"
	CategoryConsent       = "consent_required"
	CategoryPermission    = "permission_denied"
	CategoryProtocol      = "protocol_error"
	CategoryNetwork       = "network_error"
	CategoryTLS           = "tls_error"
	CategoryInvalidCursor = "invalid_cursor"
	CategoryState         = "invalid_state"
	CategoryLocal         = "local_error"
	CategoryProvider      = "provider_error"
	CategoryUnknownFolder = "unknown_folder"
)

// ErrorInfo explains an error in words a person can act on.
type ErrorInfo struct {
	Category string `json:"category"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

// Category sorts an error into one of the categories above.
func Category(err error) string {
	var consent *auth.AdminConsentError
	var pathErr *writer.PathError
	var unknown *UnknownFolderError
	switch {
	case errors.As(err, &unknown):
		return CategoryUnknownFolder
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return CategoryCanceled
	case errors.Is(err, ErrLocked):
		return CategoryLocked
	case errors.Is(err, auth.ErrCredentialNotFound):
		return CategoryCredential
	case errors.As(err, &consent) || errors.Is(err, auth.ErrAdminConsentRequired):
		return CategoryConsent
	case errors.Is(err, backend.ErrReconnectRequired) || errors.Is(err, imap.ErrAuth):
		return CategoryAuth
	case errors.Is(err, backend.ErrPermissionDenied):
		return CategoryPermission
	case errors.Is(err, imap.ErrTLS):
		return CategoryTLS
	case errors.Is(err, imap.ErrProtocol):
		return CategoryProtocol
	case errors.Is(err, backend.ErrInvalidCursor):
		return CategoryInvalidCursor
	case errors.Is(err, ErrInvalidState):
		return CategoryState
	case errors.Is(err, ErrStateWrite) || errors.As(err, &pathErr) || errors.Is(err, privatefs.ErrIntegrity) ||
		errors.Is(err, privatefs.ErrWrite) || errors.Is(err, auth.ErrInsecurePermission) || errors.Is(err, accounts.ErrRegistry):
		return CategoryLocal
	case errors.Is(err, ErrRetryExhausted) || errors.Is(err, backend.ErrRetryable):
		return CategoryNetwork
	}
	return CategoryProvider
}

// Describe explains err for an account, in words for the user.
func Describe(err error, a accounts.AccountConfig) ErrorInfo {
	info := ErrorInfo{Category: Category(err)}
	reconnect := reconnectHint(a)
	var problem *backend.Problem
	if errors.As(err, &problem) {
		info.Message, info.Hint = problem.Text, problem.Hint
		switch {
		case errors.Is(err, ErrRetryExhausted):
			info.Hint = "mailmd tried several times. Run the sync again later."
		case info.Category == CategoryAuth && a.Name != "":
			// The login worked when the account was connected, so the
			// password most likely changed.
			info.Hint = "If the password changed, enter the new one. " + reconnect
		}
		return info
	}
	var pathErr *writer.PathError
	var unknown *UnknownFolderError
	switch info.Category {
	case CategoryUnknownFolder:
		errors.As(err, &unknown)
		info.Message = fmt.Sprintf("--folder %q matches no folder of %s.", unknown.Pattern, a.Name)
		info.Hint = "The folders are: " + strings.Join(unknown.Folders, ", ")
	case CategoryCanceled:
		info.Message = "The sync was interrupted."
	case CategoryLocked:
		info.Message = "Another mailmd sync of this archive is running."
		info.Hint = "Wait until it finishes, then run the sync again."
	case CategoryCredential:
		info.Message = fmt.Sprintf("The saved login of %s is missing.", a.Name)
		info.Hint = reconnect
	case CategoryConsent:
		info.Message = "Your Microsoft organization requires an administrator to approve mailmd's read access."
		info.Hint = "Ask an administrator to open your app registration in Entra and click \"Grant admin consent\" under \"API permissions\". Then connect again."
	case CategoryAuth:
		info.Message = fmt.Sprintf("The login of %s is no longer valid.", a.Name)
		info.Hint = reconnect
		if a.Backend == "gmail" {
			info.Hint = "If your Google Cloud app is in testing mode, Google ends the login after 7 days. " + reconnect
		}
	case CategoryPermission:
		info.Message = providerLabel(a.Backend) + " did not allow mailmd to read this folder."
	case CategoryInvalidCursor:
		info.Message = "The saved position of this folder is damaged."
		info.Hint = "Move .mailmd/state.json out of the archive and sync again. Messages you already have are not written again."
	case CategoryState:
		info.Message = "The sync state file .mailmd/state.json in the archive is damaged."
		info.Hint = "Move the file out of the archive and sync again. Messages you already have are not written again."
	case CategoryLocal:
		var fsErr *fs.PathError
		switch {
		case errors.As(err, &pathErr):
			info.Message = fmt.Sprintf("Cannot write %s: %v.", pathErr.Path, cleanCause(pathErr.Err))
			info.Hint = "Check free disk space and the permissions of the archive folder."
		case errors.Is(err, auth.ErrInsecurePermission):
			info.Message = "Other users on this computer can read the saved login of " + a.Name + "."
			info.Hint = "Make the files in mailmd's config folder private (chmod 600), or connect the account again."
		case errors.Is(err, privatefs.ErrIntegrity):
			info.Message = "The archive or config folder contains a symbolic link or a file mailmd did not expect."
			info.Hint = "mailmd does not follow symbolic links inside its folders. Check the folder and try again."
		case errors.As(err, &fsErr):
			info.Message = fmt.Sprintf("Cannot write %s: %v.", fsErr.Path, fsErr.Err)
			info.Hint = "Check free disk space and the permissions of this folder."
		default:
			info.Message = "Cannot write mailmd's files: " + cleanCause(err).Error() + "."
			info.Hint = "Check free disk space and folder permissions."
		}
	case CategoryNetwork:
		info.Message = providerLabel(a.Backend) + " could not be reached."
		info.Hint = "mailmd tried several times. Check the network and run the sync again later."
	default:
		if errors.Is(err, auth.ErrOAuthSetup) {
			info.Message = fmt.Sprintf("The saved login of %s can't be used", a.Name)
			if cause := strings.TrimPrefix(cleanCause(err).Error(), "oauth setup failed"); strings.HasPrefix(cause, ": ") {
				info.Message += cause
			}
			info.Message += "."
			info.Hint = reconnect
			break
		}
		info.Message = providerLabel(a.Backend) + " returned an error: " + cleanCause(err).Error() + "."
	}
	return info
}

// reconnectHint says how to connect an account again. The account name is
// enough, because connect uses the saved server, client or client ID.
func reconnectHint(a accounts.AccountConfig) string {
	return "Run: mailmd connect " + connectKind(a.Backend) + " --account " + quoteName(a.Name)
}

// providerLabel names the provider of an account at the start of a sentence.
func providerLabel(kind string) string {
	switch kind {
	case "imap":
		return "The IMAP server"
	case "gmail":
		return "Gmail"
	case "graph":
		return "Microsoft Graph"
	}
	return "The provider"
}

func connectKind(kind string) string {
	if kind == "graph" {
		return "outlook"
	}
	return kind
}

func quoteName(name string) string {
	if strings.ContainsAny(name, " \"'$&|;<>()") {
		return `"` + strings.ReplaceAll(name, `"`, `\"`) + `"`
	}
	return name
}

// cleanCause drops the package prefixes Go errors carry, like "writer: ".
func cleanCause(err error) error {
	if err == nil {
		return errors.New("unknown error")
	}
	text := err.Error()
	for _, prefix := range []string{"writer: ", "sync: ", "imap: ", "gmail: ", "graph: ", "backend: ", "auth: ", "private storage: "} {
		text = strings.ReplaceAll(text, prefix, "")
	}
	return errors.New(strings.TrimSuffix(text, "."))
}
