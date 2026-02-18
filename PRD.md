# TUIOTP — Product Requirements Document (PRD)

> Go TUI untuk: (1) generate custom email alias via Cloudflare Email Routing, (2) monitoring & parsing OTP dari email masuk, (3) hapus alias/routing rule dari Cloudflare.

**Owner:** Apin  
**Date:** 2026-02-18  
**Status:** Draft (v1)

---

## 1. Latar Belakang & Problem

Untuk testing integrasi (SaaS, payment, social login, dsb), developer sering membuat akun sementara yang membutuhkan email untuk menerima OTP/verifikasi. Email alias manual itu:
- makan waktu,
- bikin inbox berantakan,
- sulit dilacak platform mana pakai alias apa,
- dan sering lupa dihapus.

Produk ini menyelesaikan itu dengan workflow “buat alias → tangkap OTP → hapus alias” dari satu TUI.

---

## 2. Tujuan

### 2.1 Objectives
- Membuat alias email custom secara cepat menggunakan **Cloudflare Email Routing Rules** (matcher `to` → action `forward`).
- Memantau inbox tujuan (destination) dan mendeteksi email OTP secara near real-time.
- Mem-parse OTP dengan rule yang bisa dikustomisasi (regex + template output).
- Menghapus routing rule/alias dengan aman setelah selesai.

### 2.2 Non-goals (v1)
- Mengirim email outbound memakai alias.
- Multi-user, server mode, dan dashboard web.
- Parsing AI/LLM (v1 tetap deterministic berbasis regex).

---

## 3. Scope & Persona

### Target User
- Developer / QA / SaaS founder (lokal tool) yang sering membuat akun uji / staging.

### Primary Use Case
1) Create alias `platform-rand@domain` → forward ke destination inbox.  
2) OTP masuk → tool menampilkan output custom + copy cepat.  
3) Delete alias setelah selesai.

---

## 4. User Stories

1. **Generate alias cepat**  
   Sebagai developer, aku bisa pilih platform (Shopee/Tokopedia/Telegram/custom) dan tool membuat alias otomatis.

2. **Monitor OTP masuk**  
   Sebagai developer, saat OTP email masuk, aku ingin melihat kodenya segera tanpa buka inbox.

3. **Custom output**  
   Sebagai developer, aku bisa define format output OTP (mis. `PLATFORM | CODE | TIME | ALIAS`).

4. **Cleanup**  
   Sebagai developer, aku bisa hapus alias/routing rule setelah selesai testing.

---

## 5. Requirements

### 5.1 Functional Requirements

#### A. Cloudflare / Alias Management
- **FR-CF-1**: Konfigurasi Cloudflare (API token, zone_id, domain).
- **FR-CF-2**: Pastikan destination address tersedia & (opsional) verified.
- **FR-CF-3**: Create routing rule: matcher `to` (literal) → action `forward` (destination list).
- **FR-CF-4**: List routing rules yang dibuat tool (filter `rule_name_prefix`, mis. `tuiotp:`).
- **FR-CF-5**: Delete routing rule berdasarkan selection user.
- **FR-CF-6**: Simpan `rule_id` dan metadata alias ke DB lokal.

#### B. Mailbox Monitoring
- **FR-MBX-1**: Connect ke IMAP inbox destination.
- **FR-MBX-2**: Support IMAP IDLE (push), fallback polling.
- **FR-MBX-3**: Filter email untuk alias tertentu (To:, plus optional Subject/From).
- **FR-MBX-4**: Emit event `IncomingEmail` ke pipeline parser.

#### C. OTP Parsing
- **FR-OTP-1**: Rule engine per platform (regex + criteria).
- **FR-OTP-2**: Extract OTP dari subject/body (regex tangkap).
- **FR-OTP-3**: Render output menggunakan template.
- **FR-OTP-4**: Dedupe window (hindari spam OTP sama).

#### D. TUI/UX
- **FR-UI-1**: Dashboard status (Cloudflare, destination, mailbox, parser).
- **FR-UI-2**: List alias aktif + create/delete.
- **FR-UI-3**: Menampilkan “Latest OTP” + log ringkas.
- **FR-UI-4**: OTP history view + search.
- **FR-UI-5**: Hotkey copy OTP.

### 5.2 Non-functional Requirements
- **NFR-1**: TUI tidak block: semua I/O di goroutine, UI tetap responsif.
- **NFR-2**: Secrets tidak pernah ditulis ke log.
- **NFR-3**: Retry/backoff untuk network error & rate limiting.
- **NFR-4**: DB lokal tahan crash (transaction) dan schema migration sederhana.
- **NFR-5**: Kompatibel Linux (Debian) sebagai target utama.

---

## 6. UX Spec (Wireframe)

### 6.1 Dashboard
- Header status: domain, zone, mailbox conn state
- Panel: Status, Active Aliases (selectable), Latest OTP, Logs

### 6.2 New Alias Wizard
- Step 1: pilih platform (preset + custom)
- Step 2: alias strategy (prefix-rand / custom)
- Step 3: confirm create rule (to, destination)

### 6.3 Alias Detail
- metadata alias + rule_id + enabled
- recent OTP + actions: copy / disable / delete / back

### 6.4 OTP History
- filter/search
- view raw (opsional v2), copy, back

---

## 7. Data Model (SQLite)

### 7.1 aliases
- `platform`, `alias_email`, `rule_id`, `rule_name`, `enabled`, `created_at`, `deleted_at`

### 7.2 otp_events
- `alias_email`, `platform`, `otp_code`, `received_at`, `from_email`, `subject`, `message_id`, `raw_snippet`

### 7.3 kv
- key/value untuk cache (mis. destination verified status)

---

## 8. Architecture

### 8.1 High-level
Pola Clean/Hexagonal:

- **UI (TUI/Bubble Tea)** → **Usecases** → **Ports** → **Adapters**
- Adapters:
  - Cloudflare Email Routing API client
  - IMAP watcher (IDLE/poll)
  - Parser engine (regex + template)
  - SQLite storage

### 8.2 Event Flow
- IMAP watcher menghasilkan `IncomingEmail` → Parser menghasilkan `ParsedOTP` → Storage persist → UI update.

---

## 9. Milestones (MVP)

### Sprint 1 (Alias Management)
- Cloudflare create/list/delete rules
- DB aliases
- Dashboard list aliases + wizard create + delete action

### Sprint 2 (OTP Monitoring)
- IMAP watcher IDLE + fallback poll
- Parser rules + template output
- OTP view + history + copy hotkey
- Logging + config loader

---

## 10. Risks & Mitigations
- **Destination belum verified** → tampilkan status, blok create jika `require_verified=true`.
- **IMAP disconnect** → auto reconnect & backoff.
- **Format OTP berbeda** → rules extensible via YAML, jangan hardcode.
- **Rate limit** → backoff + error surfaced di status bar.

---

## 11. References
- Cloudflare Email Routing API (Rules): https://developers.cloudflare.com/api/resources/email_routing/
- Create Routing Rule endpoint: https://developers.cloudflare.com/api/resources/email_routing/subresources/rules/methods/create/
- Bubble Tea (TUI framework): https://github.com/charmbracelet/bubbletea
- go-imap (IDLE supported): https://pkg.go.dev/github.com/emersion/go-imap
- RFC 2177 (IMAP IDLE): https://www.rfc-editor.org/rfc/rfc2177.html
