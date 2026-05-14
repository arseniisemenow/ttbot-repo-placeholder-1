package handlers

import (
	"context"
	"errors"

	s21account "github.com/arseniisemenow/s21-account-go"

	"github.com/arseniisemenow/ttbot-core/pkg/store"
)

// MigrateAdminsToS21Accounts is the one-shot bootstrap migration: copy every
// row from the legacy admins table into s21_accounts.
//
// Behavior:
//   - admins empty → no-op.
//   - For each admin row whose telegram_id is NOT already in s21_accounts:
//     upsert the corresponding s21_accounts row. CampusID/CampusName carry
//     over from the legacy row.
//   - Rows already mirrored are skipped.
//
// Idempotent. Safe to call on every cold start. The legacy admins table is
// left in place so a rollback is possible until the new path is confirmed
// healthy; a later deploy can drop the schema resource.
func MigrateAdminsToS21Accounts(ctx context.Context, st store.Store) error {
	legacy, err := st.Admins().List(ctx)
	if err != nil {
		return err
	}
	for _, a := range legacy {
		if _, err := st.S21Accounts().Get(ctx, a.TelegramID); err == nil {
			continue // already mirrored
		} else if !errors.Is(err, s21account.ErrNotFound) {
			return err
		}
		if err := st.S21Accounts().Upsert(ctx, s21account.S21Account{
			TelegramID:        a.TelegramID,
			S21Login:          a.S21Login,
			S21CredsEncrypted: a.S21CredentialsEncrypted,
			CampusID:          a.CampusID,
			CampusName:        a.CampusName,
			CreatedAt:         a.CreatedAt,
			UpdatedAt:         a.CreatedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}
