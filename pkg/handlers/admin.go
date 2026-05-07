package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/s21"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/store"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/validation"
)

// handleAdmin authenticates a user as a campus admin (DM-only). On success
// it also auto-promotes the caller's users row to a fully-verified S21 user
// whose nickname is the authenticated login. Admins can not separately use
// /provide_nickname or /remove_nickname (their identity is bound to /admin).
func (h *Handlers) handleAdmin(ctx context.Context, m *messenger.Message, args string) error {
	if err := h.captureDMChatID(ctx, m); err != nil {
		return err
	}
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

	now := h.Config.Now()

	// Check whether this campus already has an admin (and it's not the caller).
	if existing, err := h.Store.Admins().GetByCampus(ctx, profile.CampusID); err == nil {
		if existing.TelegramID != m.From.ID {
			text := profile.CampusName + " already has an admin"
			if existing.S21Login != "" {
				text += ": " + existing.S21Login
			}
			text += ". Contact this user to decide who will be the admin."
			return h.reply(ctx, m, text)
		}
		// Same admin — credential / login rotation path.
		ct, err := h.Cipher.Encrypt(password)
		if err != nil {
			return err
		}
		existing.S21Login = login
		existing.S21CredentialsEncrypted = ct
		if err := h.Store.Admins().Upsert(ctx, existing); err != nil {
			return err
		}
		// Refresh the users row so s21_nickname tracks the (possibly new) login.
		if err := h.upsertAdminUserRow(ctx, m, login, profile, now); err != nil {
			return err
		}
		return h.reply(ctx, m, "Credentials updated for "+profile.CampusName+".")
	}

	// New admin row.
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
		CreatedAt:               now,
	}
	if err := h.Store.Admins().Upsert(ctx, admin); err != nil {
		return err
	}
	if err := h.upsertAdminUserRow(ctx, m, login, profile, now); err != nil {
		return err
	}
	return h.reply(ctx, m,
		"You are now admin for "+profile.CampusName+".\n"+
			"Your S21 nickname ("+login+") is registered and verified automatically — no separate /provide_nickname needed.\n\n"+
			"Add me to a Telegram supergroup with topics enabled, then run:\n"+
			"  /bot_register_group   (anywhere in the group)\n"+
			"  /set_matches_topic    (inside the matches topic)\n"+
			"  /set_stats_topic      (inside the read-only stats topic)")
}

// upsertAdminUserRow writes the caller's users row with the credential-derived
// S21 nickname and verified_by=auth. Idempotent across re-runs of /admin.
func (h *Handlers) upsertAdminUserRow(ctx context.Context, m *messenger.Message, login string, profile s21.Profile, now time.Time) error {
	user, err := h.Store.Users().Get(ctx, m.From.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	user.TelegramID = m.From.ID
	if m.From != nil && m.From.Username != "" {
		user.TelegramUsername = m.From.Username
	}
	user.DMChatID = m.Chat.ID
	user.S21Nickname = login
	user.CampusID = profile.CampusID
	user.CampusName = profile.CampusName
	user.CoalitionName = profile.CoalitionName
	user.NicknameStatus = models.NicknameStatusProvided
	user.ProvidedBy = models.ProvidedBySelf
	user.ProvidedAt = now
	user.VerifiedBy = models.VerifiedByAuth
	user.VerifiedAt = now
	user.AdminTelegramID = m.From.ID
	return h.Store.Users().Upsert(ctx, user)
}
