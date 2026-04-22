CREATE TABLE events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    parent_id  INTEGER REFERENCES events(id) ON DELETE CASCADE,
    text       TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE event_meta (
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    key      TEXT NOT NULL,
    value    TEXT NOT NULL
);

CREATE INDEX idx_events_parent_id ON events(parent_id);
CREATE INDEX idx_events_created_at ON events(created_at);

CREATE INDEX idx_event_meta_key_value ON event_meta(key, value);
CREATE INDEX idx_event_meta_event_id ON event_meta(event_id, key, value);

CREATE VIRTUAL TABLE events_fts USING fts5(
    content,
    tokenize = "unicode61 tokenchars '=/'"
);

CREATE TRIGGER trg_events_fts_delete
AFTER DELETE ON events
BEGIN
    DELETE FROM events_fts WHERE rowid = OLD.id;
END;
