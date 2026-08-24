ALTER TABLE provision_operations
ADD COLUMN local_public_keys_json TEXT NOT NULL DEFAULT '[]';
