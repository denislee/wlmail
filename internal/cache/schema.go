package cache

const schema = `
CREATE TABLE IF NOT EXISTS messages (
    id          TEXT PRIMARY KEY,
    thread_id   TEXT NOT NULL,
    from_addr   TEXT NOT NULL DEFAULT '',
    to_addr     TEXT NOT NULL DEFAULT '',
    cc_addr     TEXT NOT NULL DEFAULT '',
    subject     TEXT NOT NULL DEFAULT '',
    snippet     TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL DEFAULT '',
    date_unix   INTEGER NOT NULL,
    labels      TEXT NOT NULL DEFAULT '[]',  -- JSON array
    has_full    INTEGER NOT NULL DEFAULT 0,  -- 1 once Get() filled body
    fetched_at  INTEGER NOT NULL,
    message_id  TEXT NOT NULL DEFAULT '',
    references_ TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS messages_date_idx ON messages(date_unix DESC);
CREATE INDEX IF NOT EXISTS messages_thread_idx ON messages(thread_id);

CREATE TABLE IF NOT EXISTS message_labels (
    message_id TEXT NOT NULL,
    label TEXT NOT NULL,
    PRIMARY KEY (message_id, label),
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS message_labels_label_idx ON message_labels(label);

CREATE TABLE IF NOT EXISTS kv (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);
`
