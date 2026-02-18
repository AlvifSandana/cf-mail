# TUIOTP — Sprint Board (Ready to Use)

Status legend:
- [ ] To Do
- [~] In Progress
- [x] Done

Owner default: `@apin` (ubah sesuai tim).

---

## Sprint 1 (Hari 1–5) — Foundation + Alias Management

### Goal
App bisa start, config valid, DB siap, Cloudflare alias CRUD jalan dari UI minimal.

### To Do
- [x] **TKT-01** Bootstrap App Entrypoint & Graceful Shutdown (M) — Owner: @apin
- [x] **TKT-02** Define Config Schema (S) — Owner: @apin
- [x] **TKT-03** Config Loader + Env Secret Resolver (M) — Owner: @apin
- [x] **TKT-04** Config Validation & Defaults (M) — Owner: @apin
- [x] **TKT-05** Domain Entities & Errors (S) — Owner: @apin
- [x] **TKT-06** Ports Interfaces (S) — Owner: @apin
- [x] **TKT-07** SQLite Migration v1 (M) — Owner: @apin
- [x] **TKT-08** SQLite Alias Repository (M) — Owner: @apin
- [x] **TKT-11** Cloudflare Client Core (M) — Owner: @apin
- [x] **TKT-12** Routing Rules CRUD (M) — Owner: @apin
- [x] **TKT-13** Destination Verification Check (M) — Owner: @apin
- [x] **TKT-14** AliasService Usecase (L) — Owner: @apin
- [x] **TKT-24** UI Root Model + Keymaps (M) — Owner: @apin
- [x] **TKT-25** UI Alias Create/Delete Flow (L) — Owner: @apin

### In Progress
- [~] (isi saat sprint berjalan)

### Done
- [x] Board initialized

### Sprint 1 Exit Criteria
- [x] Create/list/delete alias berhasil dari UI.
- [x] Rule Cloudflare tersimpan dan metadata alias masuk SQLite.
- [x] Graceful shutdown berjalan normal.

---

## Sprint 2 (Hari 6–10) — OTP Monitoring + Parser + History

### Goal
OTP masuk terdeteksi near real-time, diparse, tersimpan, tampil di UI, bisa copy.

### To Do
- [x] **TKT-15** IMAP Connector (M) — Owner: @apin
- [x] **TKT-16** IMAP Watcher IDLE + Polling fallback (L) — Owner: @apin
- [x] **TKT-17** IMAP Reconnect + Backoff (M) — Owner: @apin
- [x] **TKT-18** Email Normalization (M) — Owner: @apin
- [x] **TKT-19** Parser Engine (M) — Owner: @apin
- [x] **TKT-20** Output Template Renderer (S) — Owner: @apin
- [x] **TKT-21** Dedupe Window Logic (M) — Owner: @apin
- [x] **TKT-09** SQLite OTP Repository (M) — Owner: @apin
- [x] **TKT-22** OTPService Pipeline (L) — Owner: @apin
- [x] **TKT-26** UI OTP Latest + History + Search (M) — Owner: @apin
- [x] **TKT-27** Clipboard Adapter + Copy Hotkey (S) — Owner: @apin

### In Progress
- [~] (isi saat sprint berjalan)

### Done
- [x] Board initialized

### Updates
- [x] **TKT-24** UI Root Model + Keymaps (M)
- [x] **TKT-25** UI Alias Create/Delete Flow (L)
- [x] **TKT-26** UI OTP Latest + History + Search (M)
- [x] **TKT-27** Clipboard Adapter + Copy Hotkey (S)
- [x] **TKT-28** Structured Logging + Redaction + Health (M)
- [x] **TKT-29** Core Unit Test Suite (M)
- [x] **TKT-30** Integration & TUI Smoke Suite (L)

### Sprint 2 Exit Criteria
- [x] OTP valid masuk ke history dan latest OTP.
- [x] Dedupe aktif dan copy hotkey berfungsi.
- [x] IMAP reconnect/fallback stabil untuk skenario dasar.

---

## Sprint 3 (Hari 11–14, opsional) — Hardening + QA

### Goal
MVP stabil, observable, test suite minimum terpenuhi.

### To Do
- [x] **TKT-10** SQLite KV Repository (S) — Owner: @apin
- [x] **TKT-23** Runtime Coordinator (M) — Owner: @apin
- [x] **TKT-28** Structured Logging + Redaction + Health (M) — Owner: @apin
- [x] **TKT-29** Core Unit Test Suite (M) — Owner: @apin
- [x] **TKT-30** Integration & TUI Smoke Suite (L) — Owner: @apin

### In Progress
- [~] (isi saat sprint berjalan)

### Done
- [x] Board initialized

### Sprint 3 Exit Criteria
- [~] `go test ./... -race` internal packages lulus; full command blocked by local Go toolchain mismatch (`go1.25.7` vs `go1.25.5`).
- [x] Coverage core package baseline tersedia dan meningkat di area QA/hardening.
- [x] Tidak ada secret plaintext di log.

---

## Audit Ringkas TKT-01 s/d TKT-30

- Implemented: **TKT-01, 02, 03, 04, 07, 08, 09, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30**
- Implemented (tambahan): **TKT-05, TKT-06** sudah dipusatkan di layer `internal/domain` dan `internal/ports`
- Partial: **none**
- Missing: **none**

---

## Dependency Notes (Ringkas)
- TKT-02 -> TKT-03 -> TKT-04
- TKT-05 -> TKT-06
- TKT-07 -> TKT-08/TKT-09/TKT-10
- TKT-11 -> TKT-12/TKT-13
- TKT-12+TKT-13+TKT-08 -> TKT-14
- TKT-15 -> TKT-16 -> TKT-17/TKT-18
- TKT-19+TKT-20+TKT-21+TKT-09 -> TKT-22
- TKT-24+TKT-14 -> TKT-25
- TKT-24+TKT-22 -> TKT-26 -> TKT-27

---

## Daily Standup Template
```md
### Standup YYYY-MM-DD
- Yesterday: 
- Today:
- Blockers:
- Risks:
```
