-- Older upload records have no immutable source or endpoint binding.
-- Leave them visible in history without offering automatic recovery.
CREATE TABLE uploads (
    task_id TEXT PRIMARY KEY NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    region TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    file_id TEXT NOT NULL,
    mod_time_ns INTEGER NOT NULL,
    source_sha256 TEXT NOT NULL CHECK(length(source_sha256) = 64),
    part_size INTEGER NOT NULL CHECK(part_size > 0),
    phase TEXT NOT NULL DEFAULT 'ready'
        CHECK(phase IN ('ready','creating','active','completing','aborting')),
    cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK(cancel_requested IN (0,1)),
    last_error TEXT
);
