# TODO

Open work for agent-harness-go, in priority order. Each item says what
"done" means. Background and measurements:
[internal/websearch/README.md](internal/websearch/README.md) and
[internal/websearch/csr/README.md](internal/websearch/csr/README.md).

## Follow-ups to Phase 2.5

Phase 2.5 is done: programs are records with a lifecycle (active,
expired, stale, inactive), answers come from the index with dates and
stale warnings, long reports are read through rule-based page selection,
and a daily crawl runs under a lock (compose service, CronJob and cron
examples, the CronJob tried on kind). Reports are rechecked with
conditional requests, and page selection is measured on eight real
reports (precision 0.90). See
[internal/websearch/csr/README.md](internal/websearch/csr/README.md).
Left open:

- [ ] Run the first real extraction with programs (see "Run one real
      extraction" below) and review program names, dates and dropped
      claims, especially from report pages. Needs a Gemini API key.

**Done when:** a real crawl's programs have been reviewed by a person.

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
- Only the rule-selected pages of a long report (about 8k tokens) reach
  the model; a program described only on a page the rules score low is
  missed.
- Some company sites send an incomplete TLS certificate chain (seen on
  cp.co.id). Browsers and curl on macOS fetch the missing intermediate;
  Go does not, so those sites are unreachable until they fix their
  server.
- Single instance only: SQLite index, in-memory conversations, per-process
  rate limits. Scaling out needs shared storage (see README, "Scaling
  beyond one instance").
