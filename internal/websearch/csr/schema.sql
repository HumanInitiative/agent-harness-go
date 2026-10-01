-- Schema version 1. Applied once; later changes go in new migrations.

CREATE TABLE companies (
  id              INTEGER PRIMARY KEY,
  name            TEXT NOT NULL,
  name_normalized TEXT NOT NULL UNIQUE,
  domain          TEXT,
  domain_status   TEXT NOT NULL DEFAULT 'unverified'
                  CHECK (domain_status IN ('unverified','verified','candidate','invalid')),
  csr_url         TEXT,
  sector          TEXT NOT NULL DEFAULT '',
  region          TEXT NOT NULL DEFAULT '',
  source          TEXT NOT NULL CHECK (source IN ('seed','signal','on_demand')),
  status          TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new','verified','excluded')),
  confidence      REAL NOT NULL DEFAULT 0,
  last_crawled_at TEXT,
  next_crawl_at   TEXT,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE INDEX companies_next_crawl ON companies (next_crawl_at);

-- The routing record: every page known to hold CSR content for a company,
-- how it was found, and when to check it again. Crawls go straight to
-- these routes; discovery only runs again when they stop working.
CREATE TABLE company_pages (
  id              INTEGER PRIMARY KEY,
  company_id      INTEGER NOT NULL REFERENCES companies (id) ON DELETE CASCADE,
  url             TEXT NOT NULL,
  kind            TEXT NOT NULL CHECK (kind IN ('csr_program','report','news','foundation')),
  discovered_via  TEXT NOT NULL CHECK (discovered_via IN ('homepage','sitemap','search','probe','manual')),
  score           REAL NOT NULL DEFAULT 0,
  state           TEXT NOT NULL DEFAULT 'active'
                  CHECK (state IN ('active','pinned','gone','blocked','rejected')),
  http_status     INTEGER,
  content_hash    TEXT,
  last_checked_at TEXT,
  next_check_at   TEXT,
  failures        INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  UNIQUE (company_id, url)
);

-- Reachability per domain, so a WAF-protected or JavaScript-only site is
-- not re-fetched on every crawl.
CREATE TABLE domain_access (
  domain     TEXT PRIMARY KEY,
  status     TEXT NOT NULL CHECK (status IN ('ok','blocked','js_rendered','forbidden','unreachable','robots_disallowed')),
  detail     TEXT NOT NULL DEFAULT '',
  checked_at TEXT NOT NULL
);

CREATE TABLE evidence (
  id           INTEGER PRIMARY KEY,
  company_id   INTEGER NOT NULL REFERENCES companies (id) ON DELETE CASCADE,
  url          TEXT NOT NULL,
  title        TEXT NOT NULL DEFAULT '',
  excerpt      TEXT NOT NULL,
  kind         TEXT NOT NULL,
  fetched_at   TEXT NOT NULL,
  published_at TEXT,
  UNIQUE (company_id, url, excerpt)
);

CREATE VIRTUAL TABLE evidence_fts USING fts5(title, excerpt, content='evidence', content_rowid='id');

CREATE TRIGGER evidence_ai AFTER INSERT ON evidence BEGIN
  INSERT INTO evidence_fts (rowid, title, excerpt) VALUES (new.id, new.title, new.excerpt);
END;
CREATE TRIGGER evidence_ad AFTER DELETE ON evidence BEGIN
  INSERT INTO evidence_fts (evidence_fts, rowid, title, excerpt) VALUES ('delete', old.id, old.title, old.excerpt);
END;
CREATE TRIGGER evidence_au AFTER UPDATE ON evidence BEGIN
  INSERT INTO evidence_fts (evidence_fts, rowid, title, excerpt) VALUES ('delete', old.id, old.title, old.excerpt);
  INSERT INTO evidence_fts (rowid, title, excerpt) VALUES (new.id, new.title, new.excerpt);
END;

-- Every claim is stored as JSON {value, evidence_ids}; a claim without
-- evidence is never written.
CREATE TABLE csr_profile (
  company_id       INTEGER PRIMARY KEY REFERENCES companies (id) ON DELETE CASCADE,
  focus_areas      TEXT NOT NULL DEFAULT '[]',
  regions          TEXT NOT NULL DEFAULT '[]',
  program_types    TEXT NOT NULL DEFAULT '[]',
  known_partners   TEXT NOT NULL DEFAULT '[]',
  proposal_channel TEXT,
  seeking_partners TEXT,
  notes            TEXT NOT NULL DEFAULT '',
  extracted_at     TEXT NOT NULL,
  model_confidence REAL NOT NULL DEFAULT 0
);
