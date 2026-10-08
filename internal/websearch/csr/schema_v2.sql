-- Schema version 2: CSR programs as records with a lifecycle, and named
-- locks so two crawls never overlap.

CREATE TABLE csr_programs (
  id                INTEGER PRIMARY KEY,
  company_id        INTEGER NOT NULL REFERENCES companies (id) ON DELETE CASCADE,
  -- name_key identifies the program across extractions (see programKey).
  name_key          TEXT NOT NULL,
  name              TEXT NOT NULL,
  description       TEXT NOT NULL DEFAULT '',
  focus_areas       TEXT NOT NULL DEFAULT '[]',
  regions           TEXT NOT NULL DEFAULT '[]',
  program_types     TEXT NOT NULL DEFAULT '[]',
  -- Dates as precise as the source: YYYY, YYYY-MM or YYYY-MM-DD.
  period_start      TEXT,
  period_end        TEXT,
  proposal_deadline TEXT,
  status            TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','expired','stale','inactive')),
  misses            INTEGER NOT NULL DEFAULT 0,
  evidence_ids      TEXT NOT NULL DEFAULT '[]',
  source_urls       TEXT NOT NULL DEFAULT '[]',
  first_seen_at     TEXT NOT NULL,
  last_seen_at      TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  UNIQUE (company_id, name_key)
);

CREATE VIRTUAL TABLE csr_programs_fts USING fts5(
  name, description, focus_areas, regions, program_types,
  content='csr_programs', content_rowid='id'
);

CREATE TRIGGER csr_programs_ai AFTER INSERT ON csr_programs BEGIN
  INSERT INTO csr_programs_fts (rowid, name, description, focus_areas, regions, program_types)
  VALUES (new.id, new.name, new.description, new.focus_areas, new.regions, new.program_types);
END;
CREATE TRIGGER csr_programs_ad AFTER DELETE ON csr_programs BEGIN
  INSERT INTO csr_programs_fts (csr_programs_fts, rowid, name, description, focus_areas, regions, program_types)
  VALUES ('delete', old.id, old.name, old.description, old.focus_areas, old.regions, old.program_types);
END;
CREATE TRIGGER csr_programs_au AFTER UPDATE ON csr_programs BEGIN
  INSERT INTO csr_programs_fts (csr_programs_fts, rowid, name, description, focus_areas, regions, program_types)
  VALUES ('delete', old.id, old.name, old.description, old.focus_areas, old.regions, old.program_types);
  INSERT INTO csr_programs_fts (rowid, name, description, focus_areas, regions, program_types)
  VALUES (new.id, new.name, new.description, new.focus_areas, new.regions, new.program_types);
END;

-- A lock is held until released or until it expires, so a crashed crawl
-- cannot block the next one forever.
CREATE TABLE locks (
  name        TEXT PRIMARY KEY,
  holder      TEXT NOT NULL,
  acquired_at TEXT NOT NULL,
  expires_at  TEXT NOT NULL
);
