package handlers

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"

	"github.com/arseniisemenow/ttbot-core/pkg/identity"
	"github.com/arseniisemenow/ttbot-core/pkg/messenger"
	"github.com/arseniisemenow/ttbot-core/pkg/models"
	"github.com/arseniisemenow/ttbot-core/pkg/s21"
	"github.com/arseniisemenow/ttbot-core/pkg/validation"
)

// adminSetPromptRegex matches the bot's two-step /admin prompt header.
// Mirrors the identity-bot pattern so the reply detector can route stateless
// replies without an extra DB lookup.
var adminSetPromptRegex = regexp.MustCompile(`^\[ADMIN_OP=set\]`)

// isAdminSetReply reports whether an inbound DM is the user's credentials
// reply to the /admin force-reply prompt.
func isAdminSetReply(m *messenger.Message) bool {
	if m == nil || m.ReplyTo == nil || m.ReplyTo.From == nil || !m.ReplyTo.From.IsBot {
		return false
	}
	return adminSetPromptRegex.MatchString(m.ReplyTo.Text)
}

// handleAdminSetReply completes the /admin flow. The user's reply contains
// `login:password`. We delete the message immediately, validate against S21,
// then encrypt and upsert the admins row keyed by telegram_id.
func (h *Handlers) handleAdminSetReply(ctx context.Context, m *messenger.Message) error {
	// Best-effort scrub of the user's message — runs even if validation
	// below fails so the creds don't linger in chat history.
	defer func() {
		if err := h.M.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
			log.Printf("delete /admin creds message chat=%d msg=%d: %v", m.Chat.ID, m.MessageID, err)
		}
	}()

	login, password, err := validation.ParseAdminCredentials(strings.TrimSpace(m.Text))
	if err != nil {
		return h.reply(ctx, m, "Couldn't read creds — expected `login:password` on a single line. Run /admin again to start over.")
	}
	profile, err := h.S21.Authenticate(ctx, login, password)
	switch {
	case errors.Is(err, s21.ErrInvalidCredentials):
		return h.reply(ctx, m, "Invalid credentials. Run /admin again to retry.")
	case err != nil:
		return err
	}
	ct, err := h.Cipher.Encrypt(password)
	if err != nil {
		return err
	}
	admin := models.Admin{
		TelegramID:              m.From.ID,
		CampusID:                profile.CampusID,
		CampusName:              profile.CampusName,
		S21Login:                login,
		S21CredentialsEncrypted: ct,
		CreatedAt:               h.Config.Now(),
	}
	if err := h.Store.Admins().Upsert(ctx, admin); err != nil {
		return err
	}
	if h.Config.IdentityBaseURL != "" {
		h.SetIdentity(identity.New(h.Config.IdentityBaseURL, login, password, h.Config.IdentityAPIKey))
	}
	return h.reply(ctx, m, "Credentials registered. ttbot will use them to call the identity service.")
}
