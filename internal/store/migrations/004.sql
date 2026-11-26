CREATE TABLE transfer_options (
    singleton INTEGER PRIMARY KEY CHECK(singleton=1),
    mode TEXT NOT NULL CHECK(mode IN ('full','office','custom')),
    upload_rate INTEGER NOT NULL CHECK(upload_rate BETWEEN 0 AND 1099511627776),
    download_rate INTEGER NOT NULL CHECK(download_rate BETWEEN 0 AND 1099511627776)
);
INSERT INTO transfer_options(singleton,mode,upload_rate,download_rate) VALUES(1,'full',0,0);
