# TODO

Open work for agent-harness-go, in priority order. Each item says what
"done" means. Background and measurements:
[internal/websearch/README.md](internal/websearch/README.md) and
[internal/websearch/csr/README.md](internal/websearch/csr/README.md).

## Phase 2.5: keep the CSR index current and answer from it

The agent should answer CSR prospect questions from the local index, which a
daily crawl keeps current, and never present an expired program as open.
Today the index stores one profile per company with no notion of individual
programs, validity, or staleness, and its answers carry no dates.

### Programs as first-class records
- [ ] Add a `csr_programs` table: company, program name, focus areas,
      regions, program types, year or period (start/end), proposal deadline,
      `first_seen_at`, `last_seen_at`, status, and evidence IDs.
- [ ] Extend the extraction schema and verification to return individual
      programs; every program and every date on it must cite a verified
      excerpt (same rule as profile claims today).
- [ ] Migration v2 (via `PRAGMA user_version`) with tests that upgrade a
      v1 database.

**Done when:** "Beasiswa 2023" and "Beasiswa 2026" from the same company are
stored as two programs with their own dates and evidence.

### Program lifecycle (deprecation without deleting)
- [ ] `expired` when the end date or proposal deadline has passed: a date
      rule, no LLM involved.
- [ ] `stale` when a program is missing from one re-extraction of its
      source pages, `inactive` after two consecutive misses.
- [ ] Never delete: keep history for audit, show only active programs by
      default.

**Done when:** tests cover each transition, including a program that
reappears (back to `active`).

### Company-level freshness
- [ ] Mark a profile `stale` when its last successful extraction is older
      than 90 days (configurable) or when every route is `gone`/`blocked`.
- [ ] Stale profiles stay visible but are labelled as stale and ranked last.

**Done when:** a company whose CSR pages all disappeared is no longer shown
as a current prospect without a warning.

### Dates and DB-first answers in the tools
- [ ] `find_csr_prospects` / `check_company` show "data as of" (extraction
      date), "source checked" (last route check) and each evidence excerpt's
      fetch date.
- [ ] Filter to active programs by default; add an `include_inactive`
      argument.
- [ ] Use the existing FTS5 index for free-text program questions
      ("program beasiswa di Banten"), on top of the structured filters.
- [ ] System prompt and tool descriptions: answer CSR questions from the
      index first; use `web_search`/`web_fetch` only as a fallback, labelled
      "not yet verified in the index".

**Done when:** an end-to-end test asks a prospect question and the answer
cites evidence URLs with dates without any web request.

### Daily crawl, safely
- [ ] Locking so two crawls never overlap (a long run, or a manual run
      during the scheduled one): a lock row in SQLite, released on exit or
      expired after a timeout.
- [ ] Ship schedules: host cron example and a Kubernetes CronJob manifest
      (daily, e.g. 02:00 WIB), using the image's `csrctl crawl`.
- [ ] Size `CSR_CRAWL_COMPANIES_PER_RUN` so a daily run finishes inside its
      window at the current pacing (about 8 seconds per company per worker);
      log a warning when due companies are left over.

**Done when:** the compose and Kubernetes setups crawl daily without manual
steps, and a second concurrent `csrctl crawl` exits immediately with a clear
message.

Semantic (embedding) search is deliberately deferred: add it only if FTS5
plus synonyms proves insufficient on real questions, since it adds
embedding cost and another moving part.

## Phase 3: open discovery and on-demand lookup

From the original design (`websearch-csr-prompt.md`, section 7):

- [ ] `discover.go`: configurable query templates ("call for proposal CSR
      {tahun}", "program CSR {sektor} {wilayah}", ...) → search → fetch →
      extract which companies fund which programs → new companies with
      `source='signal'`, `status='new'`, queued for resolve + crawl.
- [ ] Flag strong, repeatedly seen signal companies as candidates for
      promotion to the seed list (promotion stays manual).
- [ ] `check_company` on-demand: when the company is not in the index,
      resolve → fetch → extract → store with `source='on_demand'` → answer.
- [ ] Review tools: `list_new_companies(limit)` and
      `set_company_status(id, status)` (status only).
- [ ] Parent vs. subsidiary ambiguity: detect similar names and shared
      brands and flag them for review instead of merging. Seen live:
      Vale Indonesia → vale.com, Indofood CBP → indofood.com.
- [ ] Deduplicate evidence by canonical URL across sources.

**Done when:** the Phase 3 acceptance tests pass (pipeline with fake
provider/extractor and fixture pages; new companies are never `verified`
automatically).

## Decisions and data waiting on people

- [ ] Replace the example values in `config/institution-profile.yaml` with
      Human Initiative's real focus areas, regions and program types: every
      fit score depends on it.
- [ ] Review domains left as `candidate` (`csrctl companies`): Amman, OCBC
      NISP, Ciputra, Matahari, XLSmart looked right but their homepages did
      not name the company in full.
- [ ] Decide whether the full seed list (`csr-seed-companies.csv`, currently
      outside the repo) should be committed, e.g. under `config/`.
- [ ] Run one real extraction with a Gemini key (`csrctl crawl` without
      `-discover-only`) on a few companies and review the stored profiles
      and dropped claims; until then extraction is only tested against a
      fake model.

## Operations and repository

- [ ] Choose where to host (recommended: a cloud VM outside Indonesian ISP
      filtering, e.g. Singapore) and deploy `deploy/docker-compose.yml`.
- [ ] Apply GitHub settings: `REQUIRED_APPROVALS=0 scripts/github-setup.sh`
      while there is a single maintainer (no ruleset is active yet, so
      direct pushes to `main` are still possible).
- [ ] Open Dependabot PRs need `@dependabot rebase` to pick up the
      commit-check fix, then review: #1 and #2 are major bumps of
      `actions/checkout` and `actions/setup-go`, #3 moves the build image to
      Go 1.27, #4 is superseded by the module updates already on `main`.
- [ ] Expose `websearch.Metrics` (fetch/search success, cache hits, blocks)
      through an endpoint or periodic log so crawl health is visible.

## Known limitations (accepted for now)

- JavaScript-only sites and WAF-protected sites cannot be fetched directly
  (about a third of the seed list); search is the way in, and extraction
  then only sees pages that are fetchable.
- Long PDF reports are truncated before extraction; program pages are
  preferred.
- Single instance only: SQLite index, in-memory conversations, per-process
  rate limits. Scaling out needs shared storage (see README, "Scaling
  beyond one instance").
