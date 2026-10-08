-- Schema version 4: open discovery. Signals record where a company was
-- seen funding or running a program (a news article, a partner's page);
-- discovery_queries and discovery_pages remember what was already searched
-- and read, so each run covers new ground. review_note carries what a
-- person should check about a company (e.g. a similar name already in the
-- index).

CREATE TABLE signals (
  id         INTEGER PRIMARY KEY,
  company_id INTEGER NOT NULL REFERENCES companies (id) ON DELETE CASCADE,
  url        TEXT NOT NULL,
  host       TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  excerpt    TEXT NOT NULL,
  program    TEXT NOT NULL DEFAULT '',
  query      TEXT NOT NULL DEFAULT '',
  seen_at    TEXT NOT NULL,
  UNIQUE (company_id, url)
);
CREATE INDEX signals_company ON signals (company_id);

CREATE TABLE discovery_queries (
  query       TEXT PRIMARY KEY,
  last_run_at TEXT NOT NULL,
  results     INTEGER NOT NULL DEFAULT 0,
  signals     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE discovery_pages (
  url     TEXT PRIMARY KEY,
  read_at TEXT NOT NULL,
  signals INTEGER NOT NULL DEFAULT 0
);

ALTER TABLE companies ADD COLUMN review_note TEXT NOT NULL DEFAULT '';
