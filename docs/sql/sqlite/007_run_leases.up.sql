CREATE TABLE IF NOT EXISTS run_leases (
 run_id TEXT PRIMARY KEY,
 owner_id TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK (epoch > 0),
 lease_until_unix_ns INTEGER NOT NULL
);
