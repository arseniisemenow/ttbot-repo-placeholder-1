package handlers

import (
	"context"
	"errors"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/identity"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/s21"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/validation"
)

// handleStart greets a DM user.
func (h *Handlers) handleStart(ctx context.Context, m *messenger.Message) error {
	return h.reply(ctx, m,
		"Hi! I track table-tennis matches at S21 campuses.\n"+
			"To register your S21 nickname, talk to @school_21_identity_bot.\n"+
			"If you administer a campus, run /admin <login:password> here.")
}

// handleAdmin stores S21 admin credentials that ttbot will use to call the
// identity service. Validation: parse login:password, attempt S21
// Authenticate (fail-closed on bad creds), encrypt, upsert admins row keyed
// by Telegram ID (last-wins on re-runs). On success, re-instantiates the
// identity-service client with the fresh credentials.
func (h *Handlers) handleAdmin(ctx context.Context, m *messenger.Message, args string) error {
	login, password, err := validation.ParseAdminCredentials(args)
	if err != nil {
		return h.reply(ctx, m, "Usage: /admin <login:password>")
	}
	profile, err := h.S21.Authenticate(ctx, login, password)
	switch {
	case errors.Is(err, s21.ErrInvalidCredentials):
		return h.reply(ctx, m, "Invalid credentials. Try again.")
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

// handleRefreshIdentity is admin-only (DM-only): flushes the in-process
// identity cache so the next /match or /rankings sees fresh data. Use after a
// nickname change in @school_21_identity_bot.
func (h *Handlers) handleRefreshIdentity(ctx context.Context, m *messenger.Message) error {
	if _, err := h.Store.Admins().Get(ctx, m.From.ID); err != nil {
		return h.reply(ctx, m, "Only admins can run /refresh_identity.")
	}
	if svc := h.Identity(); svc != nil {
		svc.Flush()
	}
	return h.reply(ctx, m, "Cache flushed.")
}
