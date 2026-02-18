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

CREATE INDEX IF NOT EXISTS idx_aliases_platform ON aliases(platform);
CREATE INDEX IF NOT EXISTS idx_aliases_deleted_at ON aliases(deleted_at);

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

CREATE INDEX IF NOT EXISTS idx_otp_events_alias_email ON otp_events(alias_email);
CREATE INDEX IF NOT EXISTS idx_otp_events_received_at ON otp_events(received_at);
CREATE INDEX IF NOT EXISTS idx_otp_events_message_id ON otp_events(message_id);

CREATE TABLE IF NOT EXISTS kv (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
