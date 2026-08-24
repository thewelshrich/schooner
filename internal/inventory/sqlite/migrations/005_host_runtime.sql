ALTER TABLE boxes ADD COLUMN runtime_path TEXT NOT NULL DEFAULT '';

ALTER TABLE observations ADD COLUMN host_runtime_path TEXT NOT NULL DEFAULT '';
ALTER TABLE observations ADD COLUMN host_runtime_version TEXT NOT NULL DEFAULT '';
ALTER TABLE observations ADD COLUMN host_protocol_version TEXT NOT NULL DEFAULT '';
ALTER TABLE observations ADD COLUMN host_capabilities_json TEXT NOT NULL DEFAULT '[]';

UPDATE boxes
SET runtime_path = COALESCE(
    (
        SELECT CASE
            WHEN observations.home = '/' THEN '/.local/bin/schooner'
            ELSE observations.home || '/.local/bin/schooner'
        END
        FROM observations
        WHERE observations.box_id = boxes.id
    ),
    ''
);

UPDATE observations
SET host_runtime_path = CASE
    WHEN home = '/' THEN '/.local/bin/schooner'
    ELSE home || '/.local/bin/schooner'
END;
