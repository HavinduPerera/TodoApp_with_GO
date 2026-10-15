-- Forward-only. The runner executes this whole file inside one transaction
-- (SQLite DDL is transactional), so a failure leaves nothing behind.
CREATE TABLE todos (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,                        -- 64-bit rowid alias; AUTOINCREMENT: an id is never reused after DELETE
    title      TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 200),  -- length() counts characters, matching Go's rune count
    done       INTEGER NOT NULL DEFAULT 0 CHECK (done IN (0, 1)),        -- SQLite has no BOOLEAN; STRICT allows only INT/INTEGER/REAL/TEXT/BLOB/ANY
    created_at TEXT    NOT NULL CHECK (length(created_at) = 27),         -- '2026-10-14T09:00:00.123456Z': fixed width, UTC, sorts as text
    updated_at TEXT    NOT NULL CHECK (length(updated_at) = 27)
) STRICT;

-- Serves WHERE done = ? ORDER BY created_at DESC, id DESC and the filtered COUNT.
-- The unfiltered list is a table scan plus a sort, which is fine below ~100k rows.
CREATE INDEX todos_done_created_idx ON todos (done, created_at DESC, id DESC);
