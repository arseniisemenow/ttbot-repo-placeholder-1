// Package ydbstore is the YDB-backed implementation of store.Store.
//
// All multi-row mutations run inside YDB SerializableReadWrite transactions
// via the ydb-go-sdk's DoTx helper. The schema this package targets lives in
// the project repo at terraform/ydb_schema.tf.
package ydbstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	s21account "github.com/arseniisemenow/s21-account-go"
	"github.com/ydb-platform/ydb-go-sdk/v3"
	"github.com/ydb-platform/ydb-go-sdk/v3/table"
	"github.com/ydb-platform/ydb-go-sdk/v3/table/result/named"
	"github.com/ydb-platform/ydb-go-sdk/v3/table/types"
	yc "github.com/ydb-platform/ydb-go-yc-metadata"

	"github.com/arseniisemenow/ttbot-core/pkg/models"
	"github.com/arseniisemenow/ttbot-core/pkg/store"
)

// Store is the YDB-backed store.
type Store struct {
	driver *ydb.Driver
}

// Open connects to YDB using Cloud Function instance metadata auth.
func Open(ctx context.Context, connectionString string) (*Store, error) {
	driver, err := ydb.Open(ctx, connectionString,
		yc.WithCredentials(),
		yc.WithInternalCA(),
	)
	if err != nil {
		return nil, fmt.Errorf("ydbstore.Open: %w", err)
	}
	return &Store{driver: driver}, nil
}

// Close shuts down the YDB driver.
func (s *Store) Close() error {
	if s.driver == nil {
		return nil
	}
	return s.driver.Close(context.Background())
}

// ----------------------------------------------------------------- helpers --

func (s *Store) doTx(ctx context.Context, fn func(ctx context.Context, tx table.TransactionActor) error) error {
	return s.driver.Table().DoTx(ctx, fn, table.WithIdempotent(),
		table.WithTxSettings(table.TxSettings(table.WithSerializableReadWrite())))
}

// doRO runs a read-only transaction.
func (s *Store) doRO(ctx context.Context, fn func(ctx context.Context, sess table.Session) error) error {
	return s.driver.Table().Do(ctx, fn, table.WithIdempotent())
}

func ts(t time.Time) types.Value {
	if t.IsZero() {
		return types.NullValue(types.TypeTimestamp)
	}
	return types.OptionalValue(types.TimestampValueFromTime(t.UTC()))
}

func optStr(s string) types.Value {
	if s == "" {
		return types.NullValue(types.TypeUTF8)
	}
	return types.OptionalValue(types.UTF8Value(s))
}

func optU64(v int64) types.Value {
	if v == 0 {
		return types.NullValue(types.TypeUint64)
	}
	return types.OptionalValue(types.Uint64Value(uint64(v)))
}

// ------------------------------------------------------------------- repos --

func (s *Store) Participants() store.ParticipantRepo            { return participantRepo{s} }
func (s *Store) Admins() store.AdminRepo                        { return adminRepo{s} }
func (s *Store) S21Accounts() store.S21AccountRepo              { return s21AccountRepo{s} }
func (s *Store) Groups() store.GroupRepo                        { return groupRepo{s} }
func (s *Store) Matches() store.MatchRepo                       { return matchRepo{s} }
func (s *Store) MatchConfirmations() store.MatchConfirmationRepo { return confirmRepo{s} }
func (s *Store) UndoCommands() store.UndoRepo                   { return undoRepo{s} }
func (s *Store) Settings() store.SettingsRepo                   { return settingsRepo{s} }

// ---------------------------------------------------------------- matchID --

// AllocateAndInsertMatch reads next_match_id, calls build(id), inserts the
// match row, and increments the counter — all in one SerializableReadWrite tx.
func (s *Store) AllocateAndInsertMatch(ctx context.Context, groupID int64, build func(uint64) models.Match) (uint64, error) {
	var allocated uint64
	err := s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		const readSQL = `
DECLARE $gid AS Uint64;
SELECT next_match_id FROM match_counters WHERE group_id = $gid;`
		res, err := tx.Execute(ctx, readSQL, table.NewQueryParameters(
			table.ValueParam("$gid", types.Uint64Value(uint64(groupID))),
		))
		if err != nil {
			return err
		}
		var next uint64 = 1
		if err := res.NextResultSetErr(ctx); err == nil && res.NextRow() {
			var n uint64
			if err := res.ScanNamed(named.OptionalWithDefault("next_match_id", &n)); err == nil {
				next = n + 1
			}
		}
		_ = res.Close()

		row := build(next)
		row.MatchID = next
		row.GroupID = groupID

		const insertSQL = `
DECLARE $group_id AS Uint64;
DECLARE $match_id AS Uint64;
DECLARE $player1_id AS Uint64;
DECLARE $player2_id AS Uint64;
DECLARE $player1_score AS Uint32;
DECLARE $player2_score AS Uint32;
DECLARE $registered_by AS Uint64;
DECLARE $status AS Utf8;
DECLARE $played_at AS Timestamp;
DECLARE $created_at AS Timestamp;
DECLARE $next AS Uint64;

UPSERT INTO matches (group_id, match_id, player1_id, player2_id, player1_score, player2_score, registered_by, status, played_at, created_at)
VALUES ($group_id, $match_id, $player1_id, $player2_id, $player1_score, $player2_score, $registered_by, $status, $played_at, $created_at);
UPSERT INTO match_counters (group_id, next_match_id) VALUES ($group_id, $next);`

		_, err = tx.Execute(ctx, insertSQL, table.NewQueryParameters(
			table.ValueParam("$group_id", types.Uint64Value(uint64(row.GroupID))),
			table.ValueParam("$match_id", types.Uint64Value(row.MatchID)),
			table.ValueParam("$player1_id", types.Uint64Value(uint64(row.Player1ID))),
			table.ValueParam("$player2_id", types.Uint64Value(uint64(row.Player2ID))),
			table.ValueParam("$player1_score", types.Uint32Value(row.Player1Score)),
			table.ValueParam("$player2_score", types.Uint32Value(row.Player2Score)),
			table.ValueParam("$registered_by", types.Uint64Value(uint64(row.RegisteredBy))),
			table.ValueParam("$status", types.UTF8Value(string(row.Status))),
			table.ValueParam("$played_at", types.TimestampValueFromTime(row.PlayedAt.UTC())),
			table.ValueParam("$created_at", types.TimestampValueFromTime(row.CreatedAt.UTC())),
			table.ValueParam("$next", types.Uint64Value(next)),
		))
		if err != nil {
			return err
		}
		allocated = next
		return nil
	})
	return allocated, err
}

// ----------------------------------------------------------- participants --

type participantRepo struct{ s *Store }

func scanParticipant(res interface {
	ScanNamed(...named.Value) error
}) (models.Participant, error) {
	var (
		p   models.Participant
		gid uint64
		tid uint64
		un  *string
		at  time.Time
	)
	if err := res.ScanNamed(
		named.Required("group_id", &gid),
		named.Required("telegram_id", &tid),
		named.Optional("telegram_username", &un),
		named.Required("activated_at", &at),
	); err != nil {
		return p, err
	}
	p.GroupID = int64(gid)
	p.TelegramID = int64(tid)
	if un != nil {
		p.TelegramUsername = *un
	}
	p.ActivatedAt = at
	return p, nil
}

func (r participantRepo) Get(ctx context.Context, gid, uid int64) (models.Participant, error) {
	var p models.Participant
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			`DECLARE $g AS Uint64; DECLARE $u AS Uint64;
			 SELECT group_id, telegram_id, telegram_username, activated_at
			 FROM participants WHERE group_id = $g AND telegram_id = $u;`,
			table.NewQueryParameters(
				table.ValueParam("$g", types.Uint64Value(uint64(gid))),
				table.ValueParam("$u", types.Uint64Value(uint64(uid))),
			))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !res.NextRow() {
			return store.ErrNotFound
		}
		p, err = scanParticipant(res)
		return err
	})
	return p, err
}

func (r participantRepo) GetByUsername(ctx context.Context, gid int64, username string) (models.Participant, error) {
	var p models.Participant
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			`DECLARE $g AS Uint64; DECLARE $u AS Utf8;
			 SELECT group_id, telegram_id, telegram_username, activated_at
			 FROM participants WHERE group_id = $g AND telegram_username = $u LIMIT 1;`,
			table.NewQueryParameters(
				table.ValueParam("$g", types.Uint64Value(uint64(gid))),
				table.ValueParam("$u", types.UTF8Value(username)),
			))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !res.NextRow() {
			return store.ErrNotFound
		}
		p, err = scanParticipant(res)
		return err
	})
	return p, err
}

func (r participantRepo) ListByGroup(ctx context.Context, gid int64) ([]models.Participant, error) {
	var out []models.Participant
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			`DECLARE $g AS Uint64;
			 SELECT group_id, telegram_id, telegram_username, activated_at
			 FROM participants WHERE group_id = $g;`,
			table.NewQueryParameters(
				table.ValueParam("$g", types.Uint64Value(uint64(gid))),
			))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		for res.NextRow() {
			p, err := scanParticipant(res)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

func (r participantRepo) Upsert(ctx context.Context, p models.Participant) error {
	const sql = `
DECLARE $g AS Uint64;
DECLARE $u AS Uint64;
DECLARE $un AS Utf8?;
DECLARE $at AS Timestamp;
UPSERT INTO participants (group_id, telegram_id, telegram_username, activated_at)
VALUES ($g, $u, $un, $at);`
	var unameVal types.Value
	if p.TelegramUsername == "" {
		unameVal = types.NullValue(types.TypeUTF8)
	} else {
		unameVal = types.OptionalValue(types.UTF8Value(p.TelegramUsername))
	}
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx, sql, table.NewQueryParameters(
			table.ValueParam("$g", types.Uint64Value(uint64(p.GroupID))),
			table.ValueParam("$u", types.Uint64Value(uint64(p.TelegramID))),
			table.ValueParam("$un", unameVal),
			table.ValueParam("$at", types.TimestampValueFromTime(p.ActivatedAt.UTC())),
		))
		return err
	})
}

// ---------------------------------------------------------------- admins --

type adminRepo struct{ s *Store }

func scanAdmin(res interface {
	ScanNamed(...named.Value) error
}) (models.Admin, error) {
	var a models.Admin
	var tid uint64
	err := res.ScanNamed(
		named.Required("telegram_id", &tid),
		named.Required("campus_id", &a.CampusID),
		named.Required("campus_name", &a.CampusName),
		named.Required("s21_login", &a.S21Login),
		named.Required("s21_credentials_encrypted", &a.S21CredentialsEncrypted),
		named.Required("created_at", &a.CreatedAt),
	)
	if err != nil {
		return a, err
	}
	a.TelegramID = int64(tid)
	return a, nil
}

func (r adminRepo) Get(ctx context.Context, id int64) (models.Admin, error) {
	var a models.Admin
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $t AS Uint64; SELECT * FROM admins WHERE telegram_id = $t;",
			table.NewQueryParameters(table.ValueParam("$t", types.Uint64Value(uint64(id)))))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !res.NextRow() {
			return store.ErrNotFound
		}
		a, err = scanAdmin(res)
		return err
	})
	return a, err
}

func (r adminRepo) GetByCampus(ctx context.Context, cid string) (models.Admin, error) {
	var a models.Admin
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $c AS Utf8; SELECT * FROM admins WHERE campus_id = $c LIMIT 1;",
			table.NewQueryParameters(table.ValueParam("$c", types.UTF8Value(cid))))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !res.NextRow() {
			return store.ErrNotFound
		}
		a, err = scanAdmin(res)
		return err
	})
	return a, err
}

func (r adminRepo) Upsert(ctx context.Context, a models.Admin) error {
	const sql = `
DECLARE $t AS Uint64; DECLARE $cid AS Utf8; DECLARE $cn AS Utf8;
DECLARE $login AS Utf8; DECLARE $enc AS Utf8; DECLARE $ca AS Timestamp;
UPSERT INTO admins (telegram_id, campus_id, campus_name, s21_login, s21_credentials_encrypted, created_at)
VALUES ($t, $cid, $cn, $login, $enc, $ca);`
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx, sql, table.NewQueryParameters(
			table.ValueParam("$t", types.Uint64Value(uint64(a.TelegramID))),
			table.ValueParam("$cid", types.UTF8Value(a.CampusID)),
			table.ValueParam("$cn", types.UTF8Value(a.CampusName)),
			table.ValueParam("$login", types.UTF8Value(a.S21Login)),
			table.ValueParam("$enc", types.UTF8Value(a.S21CredentialsEncrypted)),
			table.ValueParam("$ca", types.TimestampValueFromTime(a.CreatedAt.UTC())),
		))
		return err
	})
}

func (r adminRepo) List(ctx context.Context) ([]models.Admin, error) {
	var out []models.Admin
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"SELECT * FROM admins;", nil)
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		for res.NextRow() {
			a, err := scanAdmin(res)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	return out, err
}

// --------------------------------------------------------- s21_accounts --

type s21AccountRepo struct{ s *Store }

const s21AccountColsSel = `telegram_id, s21_login, s21_creds_encrypted, campus_id, campus_name, created_at, updated_at, last_used_at, s21_creds_failed_at, s21_creds_last_warned_at`

func scanS21Account(res interface {
	ScanNamed(...named.Value) error
}) (s21account.S21Account, error) {
	var (
		a            s21account.S21Account
		tid          uint64
		campusID     *string
		campusName   *string
		lastUsedAt   *time.Time
		failedAt     *time.Time
		lastWarnedAt *time.Time
	)
	if err := res.ScanNamed(
		named.Required("telegram_id", &tid),
		named.Required("s21_login", &a.S21Login),
		named.Required("s21_creds_encrypted", &a.S21CredsEncrypted),
		named.Optional("campus_id", &campusID),
		named.Optional("campus_name", &campusName),
		named.Required("created_at", &a.CreatedAt),
		named.Required("updated_at", &a.UpdatedAt),
		named.Optional("last_used_at", &lastUsedAt),
		named.Optional("s21_creds_failed_at", &failedAt),
		named.Optional("s21_creds_last_warned_at", &lastWarnedAt),
	); err != nil {
		return s21account.S21Account{}, err
	}
	a.TelegramID = int64(tid)
	if campusID != nil {
		a.CampusID = *campusID
	}
	if campusName != nil {
		a.CampusName = *campusName
	}
	a.LastUsedAt = lastUsedAt
	a.S21CredsFailedAt = failedAt
	a.S21CredsLastWarnedAt = lastWarnedAt
	return a, nil
}

func (r s21AccountRepo) Get(ctx context.Context, tid int64) (s21account.S21Account, error) {
	var a s21account.S21Account
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $tid AS Uint64; SELECT "+s21AccountColsSel+" FROM s21_accounts WHERE telegram_id = $tid;",
			table.NewQueryParameters(table.ValueParam("$tid", types.Uint64Value(uint64(tid)))))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !res.NextRow() {
			return s21account.ErrNotFound
		}
		a, err = scanS21Account(res)
		return err
	})
	return a, err
}

// List returns rows ordered by created_at ASC, telegram_id ASC. PickHealthy
// contract: oldest healthy row first.
func (r s21AccountRepo) List(ctx context.Context) ([]s21account.S21Account, error) {
	var out []s21account.S21Account
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"SELECT "+s21AccountColsSel+" FROM s21_accounts ORDER BY created_at, telegram_id;", nil)
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		for res.NextRow() {
			a, err := scanS21Account(res)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	return out, err
}

func (r s21AccountRepo) Upsert(ctx context.Context, a s21account.S21Account) error {
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = time.Now().UTC()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = a.UpdatedAt
	}
	optTime := func(t *time.Time) types.Value {
		if t == nil {
			return types.NullValue(types.TypeTimestamp)
		}
		return types.OptionalValue(types.TimestampValueFromTime(t.UTC()))
	}
	const sql = `
DECLARE $tid AS Uint64;
DECLARE $login AS Utf8;
DECLARE $creds AS Utf8;
DECLARE $cid AS Utf8?;
DECLARE $cname AS Utf8?;
DECLARE $cat AS Timestamp;
DECLARE $uat AS Timestamp;
DECLARE $lua AS Timestamp?;
DECLARE $fat AS Timestamp?;
DECLARE $wat AS Timestamp?;
UPSERT INTO s21_accounts
(telegram_id, s21_login, s21_creds_encrypted, campus_id, campus_name,
 created_at, updated_at, last_used_at, s21_creds_failed_at, s21_creds_last_warned_at)
VALUES ($tid, $login, $creds, $cid, $cname, $cat, $uat, $lua, $fat, $wat);`
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx, sql, table.NewQueryParameters(
			table.ValueParam("$tid", types.Uint64Value(uint64(a.TelegramID))),
			table.ValueParam("$login", types.UTF8Value(a.S21Login)),
			table.ValueParam("$creds", types.UTF8Value(a.S21CredsEncrypted)),
			table.ValueParam("$cid", optStr(a.CampusID)),
			table.ValueParam("$cname", optStr(a.CampusName)),
			table.ValueParam("$cat", types.TimestampValueFromTime(a.CreatedAt.UTC())),
			table.ValueParam("$uat", types.TimestampValueFromTime(a.UpdatedAt.UTC())),
			table.ValueParam("$lua", optTime(a.LastUsedAt)),
			table.ValueParam("$fat", optTime(a.S21CredsFailedAt)),
			table.ValueParam("$wat", optTime(a.S21CredsLastWarnedAt)),
		))
		return err
	})
}

func (r s21AccountRepo) Delete(ctx context.Context, tid int64) error {
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx,
			"DECLARE $tid AS Uint64; DELETE FROM s21_accounts WHERE telegram_id = $tid;",
			table.NewQueryParameters(table.ValueParam("$tid", types.Uint64Value(uint64(tid)))))
		return err
	})
}

// ---------------------------------------------------------------- groups --

type groupRepo struct{ s *Store }

func scanGroup(res interface {
	ScanNamed(...named.Value) error
}) (models.Group, error) {
	var g models.Group
	var gid uint64
	var adminID uint64
	var matchesTopic, statsTopic, rankingsMsg, statsMsg *uint64
	var rankingsELO, rankingsGlicko, statsELO, statsGlicko *uint64
	var timeout *uint32
	err := res.ScanNamed(
		named.Required("group_id", &gid),
		named.Required("campus_id", &g.CampusID),
		named.Required("campus_name", &g.CampusName),
		named.Required("admin_telegram_id", &adminID),
		named.Optional("matches_topic_id", &matchesTopic),
		named.Optional("stats_topic_id", &statsTopic),
		named.Optional("rankings_message_id", &rankingsMsg),
		named.Optional("stats_message_id", &statsMsg),
		named.Optional("rankings_elo_message_id", &rankingsELO),
		named.Optional("rankings_glicko_message_id", &rankingsGlicko),
		named.Optional("stats_elo_message_id", &statsELO),
		named.Optional("stats_glicko_message_id", &statsGlicko),
		named.Optional("confirmation_timeout_hours", &timeout),
		named.Required("created_at", &g.CreatedAt),
	)
	if err != nil {
		return g, err
	}
	g.GroupID = int64(gid)
	g.AdminTelegramID = int64(adminID)
	if matchesTopic != nil {
		g.MatchesTopicID = int64(*matchesTopic)
	}
	if statsTopic != nil {
		g.StatsTopicID = int64(*statsTopic)
	}
	if rankingsMsg != nil {
		g.RankingsMessageID = int64(*rankingsMsg)
	}
	if statsMsg != nil {
		g.StatsMessageID = int64(*statsMsg)
	}
	if rankingsELO != nil {
		g.RankingsELOMessageID = int64(*rankingsELO)
	}
	if rankingsGlicko != nil {
		g.RankingsGlickoMessageID = int64(*rankingsGlicko)
	}
	if statsELO != nil {
		g.StatsELOMessageID = int64(*statsELO)
	}
	if statsGlicko != nil {
		g.StatsGlickoMessageID = int64(*statsGlicko)
	}
	if timeout != nil {
		g.ConfirmationTimeoutHours = *timeout
	}
	return g, nil
}

func (r groupRepo) Get(ctx context.Context, id int64) (models.Group, error) {
	var g models.Group
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $g AS Uint64; SELECT * FROM groups WHERE group_id = $g;",
			table.NewQueryParameters(table.ValueParam("$g", types.Uint64Value(uint64(id)))))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !res.NextRow() {
			return store.ErrNotFound
		}
		g, err = scanGroup(res)
		return err
	})
	return g, err
}

func (r groupRepo) GetByCampus(ctx context.Context, cid string) (models.Group, error) {
	var g models.Group
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $c AS Utf8; SELECT * FROM groups WHERE campus_id = $c LIMIT 1;",
			table.NewQueryParameters(table.ValueParam("$c", types.UTF8Value(cid))))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !res.NextRow() {
			return store.ErrNotFound
		}
		g, err = scanGroup(res)
		return err
	})
	return g, err
}

func (r groupRepo) Upsert(ctx context.Context, g models.Group) error {
	const sql = `
DECLARE $group_id AS Uint64;
DECLARE $campus_id AS Utf8;
DECLARE $campus_name AS Utf8;
DECLARE $admin_telegram_id AS Uint64;
DECLARE $matches_topic_id AS Uint64?;
DECLARE $stats_topic_id AS Uint64?;
DECLARE $rankings_message_id AS Uint64?;
DECLARE $stats_message_id AS Uint64?;
DECLARE $rankings_elo_message_id AS Uint64?;
DECLARE $rankings_glicko_message_id AS Uint64?;
DECLARE $stats_elo_message_id AS Uint64?;
DECLARE $stats_glicko_message_id AS Uint64?;
DECLARE $confirmation_timeout_hours AS Uint32?;
DECLARE $created_at AS Timestamp;
UPSERT INTO groups (group_id, campus_id, campus_name, admin_telegram_id, matches_topic_id, stats_topic_id, rankings_message_id, stats_message_id, rankings_elo_message_id, rankings_glicko_message_id, stats_elo_message_id, stats_glicko_message_id, confirmation_timeout_hours, created_at)
VALUES ($group_id, $campus_id, $campus_name, $admin_telegram_id, $matches_topic_id, $stats_topic_id, $rankings_message_id, $stats_message_id, $rankings_elo_message_id, $rankings_glicko_message_id, $stats_elo_message_id, $stats_glicko_message_id, $confirmation_timeout_hours, $created_at);`
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		var timeoutVal types.Value
		if g.ConfirmationTimeoutHours == 0 {
			timeoutVal = types.NullValue(types.TypeUint32)
		} else {
			timeoutVal = types.OptionalValue(types.Uint32Value(g.ConfirmationTimeoutHours))
		}
		_, err := tx.Execute(ctx, sql, table.NewQueryParameters(
			table.ValueParam("$group_id", types.Uint64Value(uint64(g.GroupID))),
			table.ValueParam("$campus_id", types.UTF8Value(g.CampusID)),
			table.ValueParam("$campus_name", types.UTF8Value(g.CampusName)),
			table.ValueParam("$admin_telegram_id", types.Uint64Value(uint64(g.AdminTelegramID))),
			table.ValueParam("$matches_topic_id", optU64(g.MatchesTopicID)),
			table.ValueParam("$stats_topic_id", optU64(g.StatsTopicID)),
			table.ValueParam("$rankings_message_id", optU64(g.RankingsMessageID)),
			table.ValueParam("$stats_message_id", optU64(g.StatsMessageID)),
			table.ValueParam("$rankings_elo_message_id", optU64(g.RankingsELOMessageID)),
			table.ValueParam("$rankings_glicko_message_id", optU64(g.RankingsGlickoMessageID)),
			table.ValueParam("$stats_elo_message_id", optU64(g.StatsELOMessageID)),
			table.ValueParam("$stats_glicko_message_id", optU64(g.StatsGlickoMessageID)),
			table.ValueParam("$confirmation_timeout_hours", timeoutVal),
			table.ValueParam("$created_at", types.TimestampValueFromTime(g.CreatedAt.UTC())),
		))
		return err
	})
}

func (r groupRepo) List(ctx context.Context) ([]models.Group, error) {
	var out []models.Group
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"SELECT * FROM groups;", table.NewQueryParameters())
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		for res.NextRow() {
			g, err := scanGroup(res)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return nil
	})
	return out, err
}

// --------------------------------------------------------------- matches --

type matchRepo struct{ s *Store }

const matchColsSel = `group_id, match_id, player1_id, player2_id, player1_score, player2_score, registered_by, status, played_at, created_at`

func scanMatch(res interface {
	ScanNamed(...named.Value) error
}) (models.Match, error) {
	var m models.Match
	var gid, mid, p1, p2, regBy uint64
	var status string
	err := res.ScanNamed(
		named.Required("group_id", &gid),
		named.Required("match_id", &mid),
		named.Required("player1_id", &p1),
		named.Required("player2_id", &p2),
		named.Required("player1_score", &m.Player1Score),
		named.Required("player2_score", &m.Player2Score),
		named.Required("registered_by", &regBy),
		named.Required("status", &status),
		named.Required("played_at", &m.PlayedAt),
		named.Required("created_at", &m.CreatedAt),
	)
	if err != nil {
		return m, err
	}
	m.GroupID = int64(gid)
	m.MatchID = mid
	m.Player1ID = int64(p1)
	m.Player2ID = int64(p2)
	m.RegisteredBy = int64(regBy)
	m.Status = models.MatchStatus(status)
	return m, nil
}

func (r matchRepo) Get(ctx context.Context, gid int64, mid uint64) (models.Match, error) {
	var m models.Match
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $g AS Uint64; DECLARE $m AS Uint64; SELECT "+matchColsSel+" FROM matches WHERE group_id = $g AND match_id = $m;",
			table.NewQueryParameters(
				table.ValueParam("$g", types.Uint64Value(uint64(gid))),
				table.ValueParam("$m", types.Uint64Value(mid)),
			))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !res.NextRow() {
			return store.ErrNotFound
		}
		m, err = scanMatch(res)
		return err
	})
	return m, err
}

func (r matchRepo) UpdateStatus(ctx context.Context, gid int64, mid uint64, status models.MatchStatus) error {
	const sql = `
DECLARE $g AS Uint64; DECLARE $m AS Uint64; DECLARE $s AS Utf8;
UPDATE matches SET status = $s WHERE group_id = $g AND match_id = $m;`
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx, sql, table.NewQueryParameters(
			table.ValueParam("$g", types.Uint64Value(uint64(gid))),
			table.ValueParam("$m", types.Uint64Value(mid)),
			table.ValueParam("$s", types.UTF8Value(string(status))),
		))
		return err
	})
}

func (r matchRepo) Delete(ctx context.Context, gid int64, mid uint64) error {
	const sql = `
DECLARE $g AS Uint64; DECLARE $m AS Uint64;
DELETE FROM matches WHERE group_id = $g AND match_id = $m;
DELETE FROM match_confirmations WHERE group_id = $g AND match_id = $m;
DELETE FROM undo_commands WHERE group_id = $g AND match_id = $m;`
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx, sql, table.NewQueryParameters(
			table.ValueParam("$g", types.Uint64Value(uint64(gid))),
			table.ValueParam("$m", types.Uint64Value(mid)),
		))
		return err
	})
}

func (r matchRepo) ListByGroup(ctx context.Context, gid int64) ([]models.Match, error) {
	var out []models.Match
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $g AS Uint64; SELECT "+matchColsSel+" FROM matches WHERE group_id = $g ORDER BY played_at, match_id;",
			table.NewQueryParameters(table.ValueParam("$g", types.Uint64Value(uint64(gid)))))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		for res.NextRow() {
			m, err := scanMatch(res)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return nil
	})
	return out, err
}

func (r matchRepo) CountsByPlayer(ctx context.Context, gid int64) (map[int64]int, error) {
	// Pull (player1_id, player2_id) for non-UNDONE matches in this group, tally
	// in Go. Group volumes are small (hundreds of rows) so the row-by-row scan
	// is cheaper than two GROUP BY UNION subqueries.
	out := map[int64]int{}
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			`DECLARE $g AS Uint64;
			 SELECT player1_id, player2_id FROM matches
			 WHERE group_id = $g AND status != "UNDONE";`,
			table.NewQueryParameters(table.ValueParam("$g", types.Uint64Value(uint64(gid)))))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		for res.NextRow() {
			var p1, p2 uint64
			if err := res.ScanNamed(
				named.Required("player1_id", &p1),
				named.Required("player2_id", &p2),
			); err != nil {
				return err
			}
			out[int64(p1)]++
			out[int64(p2)]++
		}
		return nil
	})
	return out, err
}

func (r matchRepo) ListPendingExpired(ctx context.Context, before func(g models.Group) bool) ([]models.Match, error) {
	// We pull all PENDING and filter in Go (small data; saves a join).
	var pending []models.Match
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			`SELECT `+matchColsSel+` FROM matches WHERE status = "PENDING";`, nil)
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		for res.NextRow() {
			m, err := scanMatch(res)
			if err != nil {
				return err
			}
			pending = append(pending, m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	groupCache := map[int64]models.Group{}
	groupRepo := groupRepo{r.s}
	var out []models.Match
	for _, m := range pending {
		g, ok := groupCache[m.GroupID]
		if !ok {
			gg, err := groupRepo.Get(ctx, m.GroupID)
			if err != nil {
				continue
			}
			groupCache[m.GroupID] = gg
			g = gg
		}
		if !before(g) {
			continue
		}
		if now.Sub(m.CreatedAt) > g.ConfirmationTimeout() {
			out = append(out, m)
		}
	}
	return out, nil
}

// ---------------------------------------------------- match_confirmations --

type confirmRepo struct{ s *Store }

func (r confirmRepo) Insert(ctx context.Context, c models.MatchConfirmation) error {
	const sql = `
DECLARE $g AS Uint64; DECLARE $m AS Uint64; DECLARE $u AS Uint64; DECLARE $t AS Timestamp;
UPSERT INTO match_confirmations (group_id, match_id, telegram_id, confirmed_at) VALUES ($g, $m, $u, $t);`
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx, sql, table.NewQueryParameters(
			table.ValueParam("$g", types.Uint64Value(uint64(c.GroupID))),
			table.ValueParam("$m", types.Uint64Value(c.MatchID)),
			table.ValueParam("$u", types.Uint64Value(uint64(c.TelegramID))),
			table.ValueParam("$t", types.TimestampValueFromTime(c.ConfirmedAt.UTC())),
		))
		return err
	})
}

func (r confirmRepo) ListForMatch(ctx context.Context, gid int64, mid uint64) ([]models.MatchConfirmation, error) {
	var out []models.MatchConfirmation
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $g AS Uint64; DECLARE $m AS Uint64; SELECT group_id, match_id, telegram_id, confirmed_at FROM match_confirmations WHERE group_id = $g AND match_id = $m;",
			table.NewQueryParameters(
				table.ValueParam("$g", types.Uint64Value(uint64(gid))),
				table.ValueParam("$m", types.Uint64Value(mid)),
			))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		for res.NextRow() {
			var c models.MatchConfirmation
			var g, m, u uint64
			if err := res.ScanNamed(
				named.Required("group_id", &g),
				named.Required("match_id", &m),
				named.Required("telegram_id", &u),
				named.Required("confirmed_at", &c.ConfirmedAt),
			); err != nil {
				return err
			}
			c.GroupID = int64(g)
			c.MatchID = m
			c.TelegramID = int64(u)
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

func (r confirmRepo) DeleteForMatch(ctx context.Context, gid int64, mid uint64) error {
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx,
			"DECLARE $g AS Uint64; DECLARE $m AS Uint64; DELETE FROM match_confirmations WHERE group_id = $g AND match_id = $m;",
			table.NewQueryParameters(
				table.ValueParam("$g", types.Uint64Value(uint64(gid))),
				table.ValueParam("$m", types.Uint64Value(mid)),
			))
		return err
	})
}

// ----------------------------------------------------------- undo_commands --

type undoRepo struct{ s *Store }

func (r undoRepo) Insert(ctx context.Context, u models.UndoCommand) error {
	const sql = `
DECLARE $g AS Uint64; DECLARE $m AS Uint64; DECLARE $u AS Uint64; DECLARE $t AS Timestamp;
UPSERT INTO undo_commands (group_id, match_id, telegram_id, requested_at) VALUES ($g, $m, $u, $t);`
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx, sql, table.NewQueryParameters(
			table.ValueParam("$g", types.Uint64Value(uint64(u.GroupID))),
			table.ValueParam("$m", types.Uint64Value(u.MatchID)),
			table.ValueParam("$u", types.Uint64Value(uint64(u.TelegramID))),
			table.ValueParam("$t", types.TimestampValueFromTime(u.RequestedAt.UTC())),
		))
		return err
	})
}

func (r undoRepo) Delete(ctx context.Context, gid int64, mid uint64, uid int64) error {
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx,
			"DECLARE $g AS Uint64; DECLARE $m AS Uint64; DECLARE $u AS Uint64; DELETE FROM undo_commands WHERE group_id = $g AND match_id = $m AND telegram_id = $u;",
			table.NewQueryParameters(
				table.ValueParam("$g", types.Uint64Value(uint64(gid))),
				table.ValueParam("$m", types.Uint64Value(mid)),
				table.ValueParam("$u", types.Uint64Value(uint64(uid))),
			))
		return err
	})
}

func (r undoRepo) DeleteForMatch(ctx context.Context, gid int64, mid uint64) error {
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx,
			"DECLARE $g AS Uint64; DECLARE $m AS Uint64; DELETE FROM undo_commands WHERE group_id = $g AND match_id = $m;",
			table.NewQueryParameters(
				table.ValueParam("$g", types.Uint64Value(uint64(gid))),
				table.ValueParam("$m", types.Uint64Value(mid)),
			))
		return err
	})
}

func (r undoRepo) ListForMatch(ctx context.Context, gid int64, mid uint64) ([]models.UndoCommand, error) {
	var out []models.UndoCommand
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $g AS Uint64; DECLARE $m AS Uint64; SELECT group_id, match_id, telegram_id, requested_at FROM undo_commands WHERE group_id = $g AND match_id = $m;",
			table.NewQueryParameters(
				table.ValueParam("$g", types.Uint64Value(uint64(gid))),
				table.ValueParam("$m", types.Uint64Value(mid)),
			))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		for res.NextRow() {
			var u models.UndoCommand
			var g, m, uid uint64
			if err := res.ScanNamed(
				named.Required("group_id", &g),
				named.Required("match_id", &m),
				named.Required("telegram_id", &uid),
				named.Required("requested_at", &u.RequestedAt),
			); err != nil {
				return err
			}
			u.GroupID = int64(g)
			u.MatchID = m
			u.TelegramID = int64(uid)
			out = append(out, u)
		}
		return nil
	})
	return out, err
}

func (r undoRepo) ListExpired(ctx context.Context, cutoffNanos int64) ([]models.UndoCommand, error) {
	var out []models.UndoCommand
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $cut AS Timestamp; SELECT group_id, match_id, telegram_id, requested_at FROM undo_commands WHERE requested_at < $cut;",
			table.NewQueryParameters(
				table.ValueParam("$cut", types.TimestampValueFromTime(time.Unix(0, cutoffNanos).UTC())),
			))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		for res.NextRow() {
			var u models.UndoCommand
			var g, m, uid uint64
			if err := res.ScanNamed(
				named.Required("group_id", &g),
				named.Required("match_id", &m),
				named.Required("telegram_id", &uid),
				named.Required("requested_at", &u.RequestedAt),
			); err != nil {
				return err
			}
			u.GroupID = int64(g)
			u.MatchID = m
			u.TelegramID = int64(uid)
			out = append(out, u)
		}
		return nil
	})
	return out, err
}

// -------------------------------------------------------------- settings --

type settingsRepo struct{ s *Store }

func (r settingsRepo) Get(ctx context.Context, key string) (models.BotSetting, error) {
	var bs models.BotSetting
	err := r.s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
		_, res, err := sess.Execute(ctx, table.DefaultTxControl(),
			"DECLARE $k AS Utf8; SELECT key, value, updated_at, updated_by FROM bot_settings WHERE key = $k;",
			table.NewQueryParameters(table.ValueParam("$k", types.UTF8Value(key))))
		if err != nil {
			return err
		}
		defer res.Close()
		if err := res.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !res.NextRow() {
			return store.ErrNotFound
		}
		var by *uint64
		if err := res.ScanNamed(
			named.Required("key", &bs.Key),
			named.Required("value", &bs.Value),
			named.Required("updated_at", &bs.UpdatedAt),
			named.Optional("updated_by", &by),
		); err != nil {
			return err
		}
		if by != nil {
			bs.UpdatedBy = int64(*by)
		}
		return nil
	})
	return bs, err
}

func (r settingsRepo) Set(ctx context.Context, key, value string, by int64) error {
	const sql = `
DECLARE $k AS Utf8; DECLARE $v AS Utf8; DECLARE $t AS Timestamp; DECLARE $by AS Uint64?;
UPSERT INTO bot_settings (key, value, updated_at, updated_by) VALUES ($k, $v, $t, $by);`
	return r.s.doTx(ctx, func(ctx context.Context, tx table.TransactionActor) error {
		_, err := tx.Execute(ctx, sql, table.NewQueryParameters(
			table.ValueParam("$k", types.UTF8Value(key)),
			table.ValueParam("$v", types.UTF8Value(value)),
			table.ValueParam("$t", types.TimestampValueFromTime(time.Now().UTC())),
			table.ValueParam("$by", optU64(by)),
		))
		return err
	})
}

// ------------------------------------------------------------------ misc --

// EnsureSchema creates any tables that don't exist yet. We rely on terraform
// to manage the schema, so this is a no-op stub provided for symmetry.
func (s *Store) EnsureSchema(ctx context.Context) error { return nil }

// AwaitReady polls the store with a tiny query until it succeeds or ctx
// expires. Useful right after Open() in cold-start environments.
func (s *Store) AwaitReady(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for {
		err := s.doRO(ctx, func(ctx context.Context, sess table.Session) error {
			_, _, err := sess.Execute(ctx, table.DefaultTxControl(), "SELECT 1;", nil)
			return err
		})
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// silence unused import warnings if SDK signature changes.
var (
	_ = strings.Contains
	_ = errors.Is
)
