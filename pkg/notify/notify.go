// Package notify implements two-step DM-or-group notifications. DM if
// possible, otherwise post in the originating group's matches topic mentioning
// @username.
package notify

import (
	"context"
	"errors"
	"fmt"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
)

// Notifier sends DM-or-fallback messages.
type Notifier struct {
	M messenger.Messenger
}

// New returns a Notifier wired to the given messenger.
func New(m messenger.Messenger) *Notifier { return &Notifier{M: m} }

// SendUser tries to DM the user via their dm_chat_id, falling back to the
// fallback group's matches topic on failure.
//
//   - user: the recipient.
//   - fallbackGroup: the group whose matches topic is used if DM fails. May be
//     a zero Group, in which case fallback is skipped (and the original DM
//     error returned).
//   - text: the message body. The fallback prepends "@username, " (when known).
func (n *Notifier) SendUser(ctx context.Context, user models.User, fallbackGroup models.Group, text string) error {
	if user.DMChatID != 0 {
		_, err := n.M.SendMessage(ctx, user.DMChatID, 0, text)
		if err == nil {
			return nil
		}
		if !errors.Is(err, messenger.ErrForbidden) && !errors.Is(err, messenger.ErrNotFound) {
			return err
		}
		// Fall through to fallback.
	}
	if fallbackGroup.GroupID == 0 || fallbackGroup.MatchesTopicID == 0 {
		return fmt.Errorf("notify: no DM and no fallback (user=%d)", user.TelegramID)
	}
	prefix := ""
	if user.TelegramUsername != "" {
		prefix = "@" + user.TelegramUsername + ", "
	}
	_, err := n.M.SendMessage(ctx, fallbackGroup.GroupID, fallbackGroup.MatchesTopicID, prefix+text)
	return err
}
