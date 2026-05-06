# ttbot-core

Shared business-logic library for the **ttbot** Telegram table-tennis tracker
([main repo](https://github.com/arseniisemenow/ttbot)). The thin Cloud Function
entrypoints live in the main repo's `terraform/function/` (webhook) and
`terraform/cron-function/` (cron); everything else — Telegram messenger
adapter, rating engines, repositories, command handlers, S21 wrapper,
encryption, periodic-job body, and the test framework — lives here.

## Packages

| Package | What |
|---------|------|
| `pkg/models` | Domain types (User, Match, Group, …) |
| `pkg/validation` | Score/match-id regex, identifier resolution, admin-credential parsing |
| `pkg/crypto` | AES-256-GCM wrapper for admin credentials |
| `pkg/rating` | ELO and Glicko-2 engines with shared `Engine` interface |
| `pkg/messenger` | `Messenger` interface + `NewTelegram` (HTTP impl) + `Mock` |
| `pkg/s21` | `Client` interface + `NewClient` (s21auto-client-go wrapper) + `Mock` |
| `pkg/store` | Repository interfaces |
| `pkg/store/memstore` | In-memory store (used by the testkit and as the first-deploy fallback) |
| `pkg/notify` | Two-step DM-or-fallback notifier |
| `pkg/handlers` | Command dispatcher, every command, periodic job |
| `pkg/testkit` | Custom test framework: world setup, scenario DSL, assertions |

## Testing

```sh
go test ./...
```

The testkit lets a test write end-to-end scenarios with no real Telegram or
S21 calls:

```go
w := testkit.New(t)
admin := w.AddUser(50, "admin01").MakeAdmin("admin_login", "pw", "kazan", "Kazan")
g := w.AddConfiguredGroup(-1001, "kazan", "Kazan", admin.TelegramID, 5, 7)
alice := w.AddUser(100, "alice").SetNickname("alice_s21", "kazan", "Kazan", true)
bobby := w.AddUser(200, "bobby").SetNickname("bob_s21", "kazan", "Kazan", true)
g.AddPlayer(alice.TelegramID).AddPlayer(bobby.TelegramID)

w.SendInGroup(g, alice, 5, "/match @bobby 3-1")
w.AssertReplyContains("Match #1 pending")
```

## Status

- Pure logic (validation, rating, crypto): **complete with tests**.
- Messenger adapter (Telegram HTTP): complete; mock is the test-side default.
- S21 client wrapper: complete (uses
  [s21auto-client-go v0.3.2](https://github.com/arseniisemenow/s21auto-client-go)).
- Store: in-memory implementation complete; **YDB-backed implementation is the
  next milestone**. The function entrypoints currently use memstore — fine
  inside a warm Cloud Function container, wiped on cold start.
- Handlers: all commands + periodic job implemented and integration-tested
  via the testkit.
