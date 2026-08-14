CREATE TABLE IF NOT EXISTS seed_versions (
    filename varchar(255) PRIMARY KEY,
    checksum varchar(64) NOT NULL,
    applied_at timestamp NOT NULL DEFAULT now()
);