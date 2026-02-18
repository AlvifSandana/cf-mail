# TUIOTP — Implementation Backlog (MVP)

## Ringkasan
Dokumen ini adalah backlog eksekusi MVP berdasarkan PRD dan README.

Konvensi estimasi:
- **S**: ~0.5 hari
- **M**: 1–2 hari
- **L**: 2–4 hari

---

## EPIC A — Project Foundation

### TKT-01 — Bootstrap App Entrypoint & Graceful Shutdown (M)
- Buat `cmd/tuiotp/main.go` untuk init config, logger, DB, adapters, usecases, UI.
- Tambahkan graceful shutdown (OS signal + context cancel).
- Status: DONE

### TKT-02 — Define Config Schema (YAML) (S)
- Definisikan struct config sesuai kebutuhan MVP:
  - `app`, `cloudflare`, `destination`, `mailbox.imap`, `otp`, `ui`.
- Status: DONE

### TKT-03 — Config Loader + Env Secret Resolver (M)
- Load YAML config.
- Resolve env secrets (mis. `CF_API_TOKEN`, `IMAP_APP_PASSWORD`).
- Status: DONE

### TKT-04 — Config Validation & Defaults (M)
- Validasi field wajib.
- Parse duration.
- Pre-compile regex rule untuk fail-fast.
- Status: DONE

---

## EPIC B — Domain & Ports

### TKT-05 — Domain Entities & Errors (S)
- Entity: `Alias`, `OTPEvent`, `IncomingEmail`, `ParsedOTP`.
- Typed/sentinel errors domain.
- Status: DONE

### TKT-06 — Ports Interfaces (S)
- Definisikan kontrak adapter: Cloudflare, IMAP watcher, parser, repos, clipboard.
- Status: DONE

---

## EPIC C — Storage SQLite

### TKT-07 — SQLite Migration v1 (M)
- Buat schema `aliases`, `otp_events`, `kv` + migrator runner.
- Status: DONE

### TKT-08 — SQLite Alias Repository (M)
- Insert/list/soft-delete alias.
- Status: DONE

### TKT-09 — SQLite OTP Repository (M)
- Insert/query history/filter basic + helper dedupe lookup.
- Status: DONE

### TKT-10 — SQLite KV Repository (S)
- Get/set/upsert sederhana.
- Status: DONE

---

## EPIC D — Cloudflare Adapter

### TKT-11 — Cloudflare Client Core (M)
- HTTP client timeout + retry/backoff bounded.
- Status: DONE

### TKT-12 — Routing Rules CRUD (M)
- Create/list/delete rules.
- Naming convention: `tuiotp:<platform>:<alias-localpart>`.
- Status: DONE

### TKT-13 — Destination Verification Check (M)
- Gate create alias saat `require_verified=true`.
- Status: DONE

---

## EPIC E — App Usecases

### TKT-14 — AliasService Usecase (L)
- Orkestrasi create/list/delete alias.
- Status: DONE

### TKT-22 — OTPService Pipeline (L)
- IncomingEmail -> parse -> dedupe -> persist -> emit UI event.
- Status: DONE

### TKT-23 — Runtime Coordinator (M)
- Start/stop watchers + routing channel event.
- Status: DONE

---

## EPIC F — IMAP Adapter

### TKT-15 — IMAP Connector (M)
- TLS/login/select mailbox.
- Status: DONE

### TKT-16 — IMAP Watcher IDLE + Polling fallback (L)
- IDLE jika tersedia, fallback poll jika gagal.
- Status: DONE

### TKT-17 — IMAP Reconnect + Backoff (M)
- Exponential backoff + jitter.
- Status: DONE

### TKT-18 — Email Normalization (M)
- Normalisasi `to/from/subject/message_id/snippet/body`.
- Status: DONE

---

## EPIC G — Parser & Output

### TKT-19 — Parser Engine (M)
- Rule match + OTP extraction dari regex.
- Status: DONE

### TKT-20 — Output Template Renderer (S)
- Render template output (`text/template`).
- Status: DONE

### TKT-21 — Dedupe Window Logic (M)
- Dedupe by `message_id` + alias+otp window.
- Status: DONE

---

## EPIC H — UI Bubble Tea

### TKT-24 — UI Root Model + Keymaps (M)
- Dashboard skeleton + keymap global (`q ? r tab`).
- Status: DONE

### TKT-25 — UI Alias Create/Delete Flow (L)
- Form create alias, delete confirm, refresh list.
- Status: DONE

### TKT-26 — UI OTP Latest + History + Search (M)
- Panel latest OTP + history + search basic.
- Status: DONE

### TKT-27 — Clipboard Adapter + Copy Hotkey (S)
- Hotkey copy (`c`) + fallback warning.
- Status: DONE

---

## EPIC I — Observability & QA

### TKT-28 — Structured Logging + Redaction + Health (M)
- Logging JSONL + redaksi secret + status health component.
- Status: DONE

### TKT-29 — Core Unit Test Suite (M)
- Unit tests config/parser/storage/service.
- Status: DONE

### TKT-30 — Integration & TUI Smoke Suite (L)
- Integration flow dan smoke test key transitions.
- Status: DONE

---

## Milestone Mapping

### Milestone 1 — Alias Management
- TKT-01 s/d TKT-14
- TKT-24, TKT-25 (UI minimal alias)

### Milestone 2 — OTP Monitoring
- TKT-15 s/d TKT-23
- TKT-26, TKT-27

### Milestone 3 — Hardening/QA
- TKT-28, TKT-29, TKT-30
