package handlers

import (
	"context"
	"errors"
	"strings"

	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/messenger"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/models"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/s21"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/store"
	"github.com/arseniisemenow/ttbot-repo-placeholder-1/pkg/validation"
)

// handleStart greets a private-chat user and captures their dm_chat_id.
func (h *Handlers) handleStart(ctx context.Context, m *messenger.Message) error {
	if err := h.captureDMChatID(ctx, m); err != nil {
		return err
	}
	return h.reply(ctx, m,
		"Hi! I track table-tennis matches at S21 campuses.\n"+
			"Use /provide_nickname <s21_nickname> to register, or /admin <login:password> if you administer a campus.")
}

// captureDMChatID upserts the user row with their telegram_username and
// dm_chat_id. Idempotent.
func (h *Handlers) captureDMChatID(ctx context.Context, m *messenger.Message) error {
	if m.From == nil {
		return nil
	}
	user, err := h.Store.Users().Get(ctx, m.From.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	user.TelegramID = m.From.ID
	user.TelegramUsername = m.From.Username
	user.DMChatID = m.Chat.ID
	if user.NicknameStatus == "" {
		user.NicknameStatus = models.NicknameStatusNone
	}
	return h.Store.Users().Upsert(ctx, user)
}

// handleProvideNickname is /provide_nickname <s21nick>.
func (h *Handlers) handleProvideNickname(ctx context.Context, m *messenger.Message, args string) error {
	if err := h.captureDMChatID(ctx, m); err != nil {
		return err
	}
	// Admins' nickname is bound to their /admin credentials — refuse self-service.
	if _, err := h.Store.Admins().Get(ctx, m.From.ID); err == nil {
		return h.reply(ctx, m, "You're an admin; your nickname is bound to your /admin credentials. Re-run /admin <login>:<password> to change it.")
	}
	args = strings.TrimSpace(args)
	if args == "" {
		return h.reply(ctx, m, "Usage: /provide_nickname <s21_nickname>")
	}
	id, err := validation.ParseIdentifier(args)
	if err != nil || id.IsTelegram {
		return h.reply(ctx, m, "Invalid S21 nickname.")
	}
	nick := id.Value

	// Reject if same nickname already on a different user.
	if existing, err := h.Store.Users().GetByS21Nickname(ctx, nick); err == nil {
		if existing.TelegramID != m.From.ID {
			return h.reply(ctx, m, "Nickname already taken by another user.")
		}
		if existing.NicknameStatus == models.NicknameStatusProvided {
			return h.reply(ctx, m, "Nickname already registered. Use /match to play!")
		}
	}

	// Probe S21 — we need an admin token; pick the first admin we know about
	// for the *user's* eventual campus. We don't know the campus yet, so we
	// authenticate via a global probe: we need *some* admin's token. The doc
	// requires "the user's own campus admin's token" — which we resolve in
	// two steps: (1) lookup the user's campus by login using ANY known admin's
	// credentials, (2) verify the campus has a registered admin and use that
	// admin's credentials to confirm the lookup. To keep this simple we use
	// the first registered admin for cross-campus discovery, then re-check.
	admin, err := h.findFirstAdmin(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return h.reply(ctx, m, "Cannot verify nicknames right now: campus admin must register first.")
		}
		return err
	}
	adminPassword, err := h.Cipher.Decrypt(admin.S21CredentialsEncrypted)
	if err != nil {
		return err
	}
	profile, err := h.S21.LookupByLogin(ctx, admin.S21Login, adminPassword, nick)
	switch {
	case errors.Is(err, s21.ErrNotFound):
		return h.reply(ctx, m, "Invalid S21 nickname. Try again.")
	case errors.Is(err, s21.ErrInvalidCredentials):
		// Notify the admin (best-effort) and refuse this op.
		h.notifyAdminTokenExpired(ctx, admin)
		return h.reply(ctx, m, "Cannot verify nicknames right now: campus admin must run /admin again to refresh credentials. Operation aborted.")
	case err != nil:
		return err
	}

	// If user's campus has its own admin, prefer their token to re-confirm.
	if profile.CampusID != admin.CampusID {
		if campusAdmin, err := h.Store.Admins().GetByCampus(ctx, profile.CampusID); err == nil {
			pw, err := h.Cipher.Decrypt(campusAdmin.S21CredentialsEncrypted)
			if err != nil {
				return err
			}
			profile2, err := h.S21.LookupByLogin(ctx, campusAdmin.S21Login, pw, nick)
			if err == nil {
				profile = profile2
			} else if errors.Is(err, s21.ErrInvalidCredentials) {
				h.notifyAdminTokenExpired(ctx, campusAdmin)
				return h.reply(ctx, m, "Cannot verify nicknames right now: campus admin must run /admin again to refresh credentials. Operation aborted.")
			}
		}
	}

	now := h.Config.Now()
	user, _ := h.Store.Users().Get(ctx, m.From.ID)
	user.TelegramID = m.From.ID
	user.TelegramUsername = m.From.Username
	user.DMChatID = m.Chat.ID
	user.S21Nickname = profile.Login
	user.CampusID = profile.CampusID
	user.CampusName = profile.CampusName
	user.CoalitionName = profile.CoalitionName
	user.NicknameStatus = models.NicknameStatusProvided
	user.ProvidedBy = models.ProvidedBySelf
	user.ProvidedAt = now
	user.VerifiedBy = models.VerifiedByNone
	if err := h.Store.Users().Upsert(ctx, user); err != nil {
		return err
	}
	return h.reply(ctx, m, "Nickname provided! You can register matches now. Ask admin to verify you to appear in rankings.")
}

// handleRemoveNickname clears the caller's nickname.
func (h *Handlers) handleRemoveNickname(ctx context.Context, m *messenger.Message) error {
	if err := h.captureDMChatID(ctx, m); err != nil {
		return err
	}
	// Admins' nickname is bound to /admin credentials — refuse self-service.
	if _, err := h.Store.Admins().Get(ctx, m.From.ID); err == nil {
		return h.reply(ctx, m, "You're an admin; your nickname is bound to your /admin credentials and cannot be removed without losing the admin role.")
	}
	user, err := h.Store.Users().Get(ctx, m.From.ID)
	if err != nil {
		return h.reply(ctx, m, "You don't have a nickname registered.")
	}
	if user.NicknameStatus == models.NicknameStatusNone {
		return h.reply(ctx, m, "You don't have a nickname registered.")
	}
	if err := h.Store.Users().Reset(ctx, m.From.ID); err != nil {
		return err
	}
	return h.reply(ctx, m,
		"Nickname cleared. You're no longer in rankings or stats. "+
			"Run /provide_nickname <nickname> to come back.")
}

// handleProvideNicknameUser is admin-only: /provide_nickname_user @user s21nick.
func (h *Handlers) handleProvideNicknameUser(ctx context.Context, m *messenger.Message, args string) error {
	admin, err := h.Store.Admins().Get(ctx, m.From.ID)
	if err != nil {
		return h.reply(ctx, m, "You are not admin for any campus.")
	}
	parts := strings.Fields(args)
	if len(parts) != 2 {
		return h.reply(ctx, m, "Usage: /provide_nickname_user @<tgnickname> <s21_nickname>")
	}
	tgID, err := validation.ParseIdentifier(parts[0])
	if err != nil || !tgID.IsTelegram {
		return h.reply(ctx, m, "First argument must be @<tgnickname>.")
	}
	nickID, err := validation.ParseIdentifier(parts[1])
	if err != nil || nickID.IsTelegram {
		return h.reply(ctx, m, "Second argument must be a plain S21 nickname.")
	}
	target, err := h.Store.Users().GetByTelegramUsername(ctx, tgID.Value)
	if err != nil {
		return h.reply(ctx, m, "User not found. Ask them to DM me first.")
	}
	pw, err := h.Cipher.Decrypt(admin.S21CredentialsEncrypted)
	if err != nil {
		return err
	}
	profile, err := h.S21.LookupByLogin(ctx, admin.S21Login, pw, nickID.Value)
	switch {
	case errors.Is(err, s21.ErrNotFound):
		return h.reply(ctx, m, "Invalid S21 nickname. Try again.")
	case errors.Is(err, s21.ErrInvalidCredentials):
		h.notifyAdminTokenExpired(ctx, admin)
		return h.reply(ctx, m, "Cannot verify nicknames right now: your S21 token is expired. Run /admin again.")
	case err != nil:
		return err
	}
	now := h.Config.Now()
	target.S21Nickname = profile.Login
	target.CampusID = profile.CampusID
	target.CampusName = profile.CampusName
	target.CoalitionName = profile.CoalitionName
	target.NicknameStatus = models.NicknameStatusProvided
	target.ProvidedBy = models.ProvidedByAdmin
	target.ProvidedAt = now
	target.VerifiedBy = models.VerifiedByAdmin
	target.VerifiedAt = now
	target.AdminTelegramID = admin.TelegramID
	if err := h.Store.Users().Upsert(ctx, target); err != nil {
		return err
	}
	return h.reply(ctx, m, "Nickname provided and verified for @"+target.TelegramUsername+".")
}

// handleVerifyNickname promotes an already-provided user to verified.
func (h *Handlers) handleVerifyNickname(ctx context.Context, m *messenger.Message, args string) error {
	admin, err := h.Store.Admins().Get(ctx, m.From.ID)
	if err != nil {
		return h.reply(ctx, m, "You are not admin for any campus.")
	}
	parts := strings.Fields(args)
	if len(parts) != 2 {
		return h.reply(ctx, m, "Usage: /verify_nickname @<tgnickname> <s21_nickname>")
	}
	tgID, _ := validation.ParseIdentifier(parts[0])
	nickID, _ := validation.ParseIdentifier(parts[1])
	if !tgID.IsTelegram || nickID.IsTelegram {
		return h.reply(ctx, m, "Usage: /verify_nickname @<tgnickname> <s21_nickname>")
	}
	target, err := h.Store.Users().GetByTelegramUsername(ctx, tgID.Value)
	if err != nil {
		return h.reply(ctx, m, "User not found. Ask them to DM me first.")
	}
	if target.NicknameStatus != models.NicknameStatusProvided {
		return h.reply(ctx, m, "User has no nickname to verify. Ask them to /provide_nickname first.")
	}
	if !strings.EqualFold(target.S21Nickname, nickID.Value) {
		return h.reply(ctx, m, "Nickname mismatch.")
	}
	target.VerifiedBy = models.VerifiedByAdmin
	target.VerifiedAt = h.Config.Now()
	target.AdminTelegramID = admin.TelegramID
	if err := h.Store.Users().Upsert(ctx, target); err != nil {
		return err
	}
	return h.reply(ctx, m, "Nickname verified for @"+target.TelegramUsername+" ("+target.S21Nickname+").")
}

// handleGuest is admin-only: /guest @user.
func (h *Handlers) handleGuest(ctx context.Context, m *messenger.Message, args string) error {
	admin, err := h.Store.Admins().Get(ctx, m.From.ID)
	if err != nil {
		return h.reply(ctx, m, "You are not admin for any campus.")
	}
	parts := strings.Fields(args)
	if len(parts) != 1 {
		return h.reply(ctx, m, "Usage: /guest @<tgnickname>")
	}
	id, err := validation.ParseIdentifier(parts[0])
	if err != nil || !id.IsTelegram {
		return h.reply(ctx, m, "Argument must be @<tgnickname>.")
	}
	target, err := h.Store.Users().GetByTelegramUsername(ctx, id.Value)
	if err != nil {
		return h.reply(ctx, m, "User not found. Ask them to DM me first.")
	}
	now := h.Config.Now()
	target.NicknameStatus = models.NicknameStatusGuest
	target.ProvidedBy = models.ProvidedByAdmin
	target.ProvidedAt = now
	target.VerifiedBy = models.VerifiedByAdmin
	target.VerifiedAt = now
	target.AdminTelegramID = admin.TelegramID
	target.S21Nickname = ""
	target.CampusID = ""
	target.CampusName = ""
	target.CoalitionName = ""
	if err := h.Store.Users().Upsert(ctx, target); err != nil {
		return err
	}
	return h.reply(ctx, m, "Guest created: @"+target.TelegramUsername+".")
}

// findFirstAdmin returns any registered admin (used as a token source for
// initial campus discovery, before we know the user's campus).
func (h *Handlers) findFirstAdmin(ctx context.Context) (models.Admin, error) {
	admins, err := h.Store.Admins().List(ctx)
	if err != nil {
		return models.Admin{}, err
	}
	if len(admins) == 0 {
		return models.Admin{}, store.ErrNotFound
	}
	return admins[0], nil
}

func (h *Handlers) notifyAdminTokenExpired(ctx context.Context, admin models.Admin) {
	user, err := h.Store.Users().Get(ctx, admin.TelegramID)
	if err != nil {
		return
	}
	_ = h.Notifier.SendUser(ctx, user, models.Group{}, "Your S21 token expired. Please run /admin <login:password> to refresh credentials.")
}
