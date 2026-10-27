package gmail

import (
	"fmt"

	mdbackend "github.com/vincentsch/mailmd/internal/backend"
)

const accountScopeKey = "gmail-v1:account"

func validateFetchScope(scopeKey string) error {
	if scopeKey != accountScopeKey {
		return fmt.Errorf("gmail: fetch scope not found: %w", mdbackend.ErrScopeNotFound)
	}
	return nil
}
