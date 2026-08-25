ALTER TABLE boxes ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1));

CREATE UNIQUE INDEX one_default_box
ON boxes(is_default)
WHERE is_default = 1;
