CREATE TABLE connections (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    provider TEXT NOT NULL,
    region TEXT NOT NULL,
    endpoint TEXT NOT NULL
);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY NOT NULL,
    connection_id TEXT NOT NULL REFERENCES connections(id),
    direction TEXT NOT NULL CHECK(direction IN ('upload','download')),
    bucket TEXT NOT NULL,
    object_key TEXT NOT NULL,
    local_path TEXT NOT NULL,
    size INTEGER NOT NULL CHECK(size >= 0),
    state TEXT NOT NULL CHECK(state IN ('queued','running','paused','failed','completed','canceled')),
    upload_id TEXT NOT NULL DEFAULT ''
);

CREATE TABLE parts (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    number INTEGER NOT NULL CHECK(number > 0),
    offset INTEGER NOT NULL CHECK(offset >= 0),
    size INTEGER NOT NULL CHECK(size > 0),
    etag TEXT NOT NULL,
    checksum TEXT NOT NULL,
    PRIMARY KEY(task_id, number)
);
