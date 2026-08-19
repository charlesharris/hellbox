-- hellbox state.
--
-- This database is an index, not the system of record. Every rip directory
-- carries a complete disc.json and the verbatim makemkvcon output, so the
-- contents here can be rebuilt from the filesystem if it is ever lost. Nothing
-- may depend on state that exists only in this file.

CREATE TABLE IF NOT EXISTS drives (
    id            INTEGER PRIMARY KEY,
    stable_id     TEXT    NOT NULL UNIQUE,
    device_path   TEXT,
    label         TEXT,
    vendor_model  TEXT,
    first_seen    INTEGER NOT NULL,
    last_seen     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS discs (
    id            INTEGER PRIMARY KEY,
    fingerprint   TEXT    NOT NULL UNIQUE,
    volume_label  TEXT,
    disc_type     TEXT,
    title_count   INTEGER NOT NULL DEFAULT 0,
    total_bytes   INTEGER NOT NULL DEFAULT 0,
    first_seen    INTEGER NOT NULL,
    rip_dir       TEXT,
    makemkv_info  TEXT,

    -- The last job that existed when the disc was forgotten. Jobs up to and
    -- including it do not count against max_rip_attempts: forgetting a disc is
    -- a person saying to try it again, which the cap would otherwise refuse.
    --
    -- A job id rather than a timestamp, because a forget and the retry it
    -- invites happen in the same second and second-granularity clocks cannot
    -- separate them. Ids are monotonic and exact.
    forgotten_after_job INTEGER,

    -- Which mechanism read this disc: native-dvd, native-bluray-aacs,
    -- decrypt-copy, makemkv.
    --
    -- Recorded so the collection can report on itself. Nobody owns a list of
    -- which of their discs carry BD+, and asking them to know is the wrong
    -- question -- but counting the discs that needed MakeMKV gives exactly the
    -- number that matters: how much of the library stops working when a
    -- registration key expires.
    read_path TEXT,

    -- The disc's own name, where it has one. Only Blu-rays carry this, in
    -- BDMV/META/DL/bdmt_eng.xml, and it is far better evidence than a volume
    -- label: "FIREFLY: DISC 1" against "FIREFLYUS_D1". Kept separate from
    -- volume_label because they disagree and both are worth having.
    disc_name TEXT
);

CREATE TABLE IF NOT EXISTS jobs (
    id            INTEGER PRIMARY KEY,
    disc_id       INTEGER NOT NULL REFERENCES discs(id),
    drive_id      INTEGER NOT NULL REFERENCES drives(id),
    state         TEXT    NOT NULL,
    attempt       INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL,
    started_at    INTEGER,
    ended_at      INTEGER,
    error         TEXT,
    titles_total  INTEGER NOT NULL DEFAULT 0,
    titles_done   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS jobs_disc_idx  ON jobs(disc_id);
CREATE INDEX IF NOT EXISTS jobs_state_idx ON jobs(state);

-- One row per title to transcode. Per title rather than per disc so a title
-- that fails is the only one retried, and so a disc is not re-encoded whole to
-- recover one file.
CREATE TABLE IF NOT EXISTS transcodes (
    id           INTEGER PRIMARY KEY,
    disc_id      INTEGER NOT NULL REFERENCES discs(id),
    title_index  INTEGER NOT NULL,
    state        TEXT    NOT NULL,
    profile      TEXT    NOT NULL,
    source_path  TEXT    NOT NULL,
    output_path  TEXT,
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    attempt      INTEGER NOT NULL DEFAULT 0,
    hardware     INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    started_at   INTEGER,
    ended_at     INTEGER,
    error        TEXT,

    -- A disc's title is queued once. Re-queuing an existing one updates it
    -- rather than adding a second job for the same file.
    UNIQUE(disc_id, title_index)
);

CREATE INDEX IF NOT EXISTS transcodes_state_idx ON transcodes(state);

CREATE TABLE IF NOT EXISTS titles (
    id             INTEGER PRIMARY KEY,
    disc_id        INTEGER NOT NULL REFERENCES discs(id),
    title_index    INTEGER NOT NULL,
    duration_secs  INTEGER NOT NULL DEFAULT 0,
    chapters       INTEGER NOT NULL DEFAULT 0,
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    source_file    TEXT,
    output_file    TEXT,
    ripped         INTEGER NOT NULL DEFAULT 0,
    verified       INTEGER NOT NULL DEFAULT 0,
    UNIQUE(disc_id, title_index)
);

-- Stream inventory. Phase 1 does not read this; it exists because identifying
-- what a disc contains means reasoning over runtimes and stream layouts, and
-- capturing it while the disc is in the drive costs nothing.
CREATE TABLE IF NOT EXISTS streams (
    id            INTEGER PRIMARY KEY,
    title_id      INTEGER NOT NULL REFERENCES titles(id) ON DELETE CASCADE,
    stream_index  INTEGER NOT NULL,
    kind          TEXT,
    codec         TEXT,
    language      TEXT,
    channels      INTEGER,
    resolution    TEXT,
    frame_rate    TEXT,
    flags         TEXT,
    UNIQUE(title_id, stream_index)
);

-- What has been filed into the library, so nothing is filed twice and a file
-- renamed by hand is never clobbered.
CREATE TABLE IF NOT EXISTS library_links (
    id           INTEGER PRIMARY KEY,
    disc_id      INTEGER NOT NULL REFERENCES discs(id),
    title_index  INTEGER NOT NULL,
    path         TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    UNIQUE(disc_id, title_index)
);

CREATE TABLE IF NOT EXISTS events (
    id        INTEGER PRIMARY KEY,
    ts        INTEGER NOT NULL,
    job_id    INTEGER REFERENCES jobs(id),
    drive_id  INTEGER REFERENCES drives(id),
    level     TEXT NOT NULL,
    message   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS events_ts_idx ON events(ts DESC);

CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER NOT NULL
);
