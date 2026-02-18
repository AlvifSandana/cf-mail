# TUIOTP (Go TUI) — Cloudflare Email Alias + OTP Monitor

TUIOTP adalah tool **Go TUI** untuk:
1) **Generate email alias** lewat **Cloudflare Email Routing** (create routing rule),
2) **Monitor email masuk** (IMAP IDLE/poll) dan **parse OTP**,
3) **Delete alias** (hapus routing rule) untuk cleanup.

> Cloudflare Email Routing Rules: matcher `to` + action `forward`.  
> Docs: https://developers.cloudflare.com/api/resources/email_routing/subresources/rules/methods/create/

---

## Features (MVP)
- Create/List/Delete Cloudflare Email Routing rules (alias management)
- IMAP watcher (IDLE jika tersedia, fallback polling)
- OTP parser berbasis rules (regex) + template output custom
- OTP history (SQLite) + search
- Copy OTP hotkey di TUI

---

## Tech Stack
- Go
- TUI: Bubble Tea (Charmbracelet) — https://github.com/charmbracelet/bubbletea
- IMAP: go-imap (IDLE supported) — https://pkg.go.dev/github.com/emersion/go-imap
- DB: SQLite
- Config: YAML

---

## Repo Structure (recommended)

```
/cmd/tuiotp/                 # main
/internal/
  /ui/                       # BubbleTea models, views
  /app/                      # usecases
  /domain/                   # entities: Alias, OTPEvent
  /ports/                    # interfaces
  /adapters/
     /cloudflare/            # API client
     /mailbox/imap/          # IMAP watcher
     /parser/                # regex+template engine
  /storage/                  # sqlite migrations + repo
/config/                     # sample config yaml
```

---

## Quick Start

### 1) Prerequisites
- Domain sudah aktif di Cloudflare + Email Routing enabled.
- Cloudflare API Token dengan permission yang cukup untuk Email Routing.
- IMAP credentials untuk destination inbox (disarankan app password jika Gmail).

### 2) Setup environment variables
```bash
export CF_API_TOKEN="..."
export IMAP_APP_PASSWORD="..."
```

### 3) Config
Buat `config.yml` berdasarkan contoh di bawah.

### 4) Run
```bash
go run ./cmd/tuiotp --config ./config.yml
```

---

## Sample `config.yml`

```yaml
app:
  timezone: "Asia/Jakarta"
  db_path: "./tuiotp.db"
  log_path: "./tuiotp.log"

cloudflare:
  api_token_env: "CF_API_TOKEN"
  account_id: "xxxx"
  zone_id: "yyyy"
  domain: "example.com"
  rule_name_prefix: "tuiotp"
  default_priority: 0
  enabled_by_default: true

destination:
  email: "apin.inbox@gmail.com"
  require_verified: true

mailbox:
  mode: "imap"
  imap:
    host: "imap.gmail.com"
    port: 993
    tls: true
    username: "apin.inbox@gmail.com"
    password_env: "IMAP_APP_PASSWORD"
    mailbox: "INBOX"
    idle: true
    poll_interval: "5s"
    fetch_body: "text"

otp:
  output_format: "{{.Platform}} | {{.OTP}} | {{.ReceivedAt}} | {{.Alias}}"
  dedupe_window: "2m"
  rules:
    - platform: "SHOPEE"
      from_contains: ["shopee", "no-reply"]
      subject_regex: "(?i)kode|otp|verifikasi"
      otp_regex: "\\b(\\d{6})\\b"
    - platform: "TOKOPED"
      from_contains: ["tokopedia"]
      otp_regex: "\\b(\\d{6})\\b"
    - platform: "TELEGRAM"
      subject_regex: "(?i)Telegram"
      otp_regex: "\\b(\\d{5})\\b"
    - platform: "CUSTOM"
      subject_regex: ".*"
      otp_regex: "\\b(\\d{4,8})\\b"

ui:
  clipboard:
    enabled: true
    method: "auto"
```

---

## TUI Keybindings (proposal)

Global:
- `q` quit
- `?` toggle help
- `r` refresh cloudflare list
- `tab` switch panel (optional)

Dashboard:
- `n` new alias
- `d` delete selected alias
- `enter` alias detail
- `o` otp history

Detail/History:
- `c` copy OTP
- `b` / `esc` back

---

## SQLite Schema (MVP)

`aliases`
```sql
CREATE TABLE IF NOT EXISTS aliases (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  platform TEXT NOT NULL,
  alias_email TEXT NOT NULL UNIQUE,
  rule_id TEXT NOT NULL,
  rule_name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  deleted_at TEXT
);
```

`otp_events`
```sql
CREATE TABLE IF NOT EXISTS otp_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  alias_email TEXT NOT NULL,
  platform TEXT NOT NULL,
  otp_code TEXT NOT NULL,
  received_at TEXT NOT NULL,
  from_email TEXT,
  subject TEXT,
  message_id TEXT,
  raw_snippet TEXT
);
```

---

## Architecture Notes

### Cloudflare Adapter
- Operasi utama:
  - create/list/delete routing rules untuk alias.
- Rule naming:
  - `tuiotp:<platform>:<alias>` agar mudah difilter saat list.

Docs:
- Email routing API: https://developers.cloudflare.com/api/resources/email_routing/
- Create routing rule: https://developers.cloudflare.com/api/resources/email_routing/subresources/rules/methods/create/

### IMAP Monitoring
- Gunakan IMAP IDLE (push) jika server support.
- Spec: RFC 2177 https://www.rfc-editor.org/rfc/rfc2177.html

### OTP Parser
- Rule-based (regex):
  - match by From/Subject
  - extract OTP by regex capture group
  - render output via `text/template`

---

## Security
- Jangan hardcode token/password di file repo.
- Prefer env vars (`CF_API_TOKEN`, `IMAP_APP_PASSWORD`).
- Log harus redacted (no secrets).

---

## Roadmap (nice to have)
- Auto TTL cleanup (hapus alias setelah X waktu)
- Gmail API mode
- Export OTP history (CSV/JSON)
- Notifications (desktop)

---

## License
TBD
