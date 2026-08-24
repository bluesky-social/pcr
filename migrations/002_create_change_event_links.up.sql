CREATE TABLE change_event_links (
    event_id TEXT    NOT NULL REFERENCES change_events(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    label    TEXT    NOT NULL DEFAULT '',
    url      TEXT    NOT NULL,
    PRIMARY KEY (event_id, position)
);
