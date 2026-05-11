// Package notify posts user-facing notifications. DMs are no longer used;
// every notification lands in the originating group's matches topic with an
// @username mention when one is known.
package notify

import (
	"context"
	"fmt"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
)

// Notifier sends group-targeted notifications.
type Notifier struct {
	M messenger.Messenger
}

// New returns a Notifier wired to the given messenger.
func New(m messenger.Messenger) *Notifier { return &Notifier{M: m} }

// SendInGroup posts `text` in the group's matches topic, prefixed with
// `@username, ` when telegramUsername is non-empty.
//
// `telegramUsername` should come from the participants table when known. If
// the group has no matches topic configured, the call is a no-op (returning
// nil) — we'd have nowhere to post.
func (n *Notifier) SendInGroup(ctx context.Context, group models.Group, telegramUsername, text string) error {
	if group.GroupID == 0 || group.MatchesTopicID == 0 {
		return fmt.Errorf("notify: group has no matches topic (group=%d)", group.GroupID)
	}
	prefix := ""
	if telegramUsername != "" {
		prefix = "@" + telegramUsername + ", "
	}
	_, err := n.M.SendMessage(ctx, group.GroupID, group.MatchesTopicID, prefix+text)
	return err
}
