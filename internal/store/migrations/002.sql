-- M0 tasks have no bound remote identity. Only new downloads can be resumed.
CREATE TABLE downloads (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    region TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    etag TEXT NOT NULL,
    version_id TEXT NOT NULL,
    partial_path TEXT UNIQUE NOT NULL,
    file_id TEXT NOT NULL,
    cleanup_pending INTEGER NOT NULL DEFAULT 1,
    range_size INTEGER NOT NULL CHECK(range_size > 0),
    last_error TEXT
);
