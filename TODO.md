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

## Phase 3 follow-ups

Phase 3 is done: open discovery (`csrctl discover`, signals, promotion
candidates), on-demand lookups in `check_company`, the review tools
(`list_new_companies`, `set_company_status` for reviewer keys only),
review notes for similar names and shared or group domains, and
document-level deduplication. See
[internal/websearch/csr/README.md](internal/websearch/csr/README.md),
"Beyond the seed list". Left open:

- [ ] Run discovery for real (needs a Gemini key and a SearXNG instance
      outside ISP filtering) and review the first signals and new
      companies; tune `config/discovery.yaml` and `minSignalPageScore`
      (`discover.go`) on what it finds.
- [ ] Decide `CSR_REVIEWER_KEY_IDS` (who may verify companies through the
      agent) and whether to enable `CSR_ON_DEMAND_ENABLED` in production.
- [ ] A foreign parent's global site (Vale Indonesia on vale.com) is not
      flagged; a rule on name and domain alone flagged only Indonesian
      companies using .com. Detecting it needs the homepage content (a
      site about many countries).

## Pilot 1: donation reporting agent

Goal: a program team picks a program and a period and gets a draft report
to review. Figures come from the donation data; the narrative sections are
written by the model from the program's activity reports (pdf, docx) and
cite them. Project-level plan and owners: the shared TODO document ("TODO
Proyek Agentic AI & DevOps"). How it works, the endpoint and the layout:
[docs/donation-report-template.md](docs/donation-report-template.md).

The flow is built end to end and tested with a fake model:
`POST /api/v1/reports` (behind `REPORT_TOOLS_ENABLED`) returns the draft as
JSON or as a `.docx`. Figures and activity documents are read over HTTP from
a mock of the donation API (`make mock-api`, `REPORT_API_URL`; contract in
[docs/donation-data-api.md](docs/donation-data-api.md)). What is left needs
the real donation API, a Gemini key and the program team. Rule that shapes everything: **figures are
filled by code from the donation data, never written by the model.**

### Working defaults to confirm

These were chosen to get the flow working; each is cheap to change.

- [ ] Activity documents live in a folder per program
      (`REPORT_DOCS_DIR/<program id>/`). Confirm with the program team who
      puts files there, or replace the adapter with an upload or a document
      store behind `outbound.ActivityDocuments`.
- [ ] The report is generated in code and rendered to `.docx` in-process
      (no Python sidecar). If the team wants to edit the template layout
      themselves, switch to a `.docx` template with placeholders. PDF output
      is not built (convert the `.docx`, or add a renderer).
- [ ] Section list and order, confirmed by the program team (see the
      template document). Headings are English; the report is for Indonesian
      readers, so decide whether to translate them.
- [ ] Who receives the report (individual donor, corporate donor, internal);
      it sets the tone in the prompt
      (`internal/application/reporting/narrative.go`).

### Needs the donation API owner

- [ ] Show the owner the proposed contract
      ([docs/donation-data-api.md](docs/donation-data-api.md)) and get their
      real endpoints, auth, fields and one sample response per program; then
      either they match it or `internal/adapters/outbound/reportapi` is
      adjusted. Until then everything runs against the mock.
- [ ] Point `REPORT_API_URL` / `REPORT_API_KEY` at the real API and compare
      a report's figures with the donation system by hand.
- [ ] Read-only access for the harness, and permission to send activity
      text (not donor data) to the model.

**Done when:** a request for a real program and period returns figures that
match the donation system, and the activity documents are the program's real
reports.

### Needs a Gemini key and past reports

- [ ] Try the narrative with the real model on 5 to 10 past reports and read
      the output: tone, language, invented facts, whether the number check
      flags the right things. Tune the prompt.
- [ ] Eval set: 20 to 30 cases from past reports (program, period, expected
      figures, a reference narrative). Figures must match 100%; narrative
      reviewed by the Penanggung Jawab, target 80% accepted.
- [ ] Install `pdftotext` (poppler-utils) in the deployed image: without it
      PDFs are skipped with a note in the draft. A scanned PDF has no text
      and is skipped with a note too (OCR is out of scope).
- [ ] Check cost and time of one report; adjust `reporting.DefaultConfig`
      (documents, characters) and `REQUEST_TIMEOUT_SECONDS`.

**Done when:** the eval has run on at least five past reports and a person
has reviewed the failures.

### Later

- [x] Entry point for users: the page at `/reports` lists programs (from
      `GET /api/v1/programs`), takes a period and shows or downloads the
      draft. Tried in headless Chrome against the mock API.
- [ ] Show the page to the program team and adjust the wording and flow; it
      is in Indonesian while the draft is not yet.
- [ ] The page asks for the API key every time a tab opens. If that is too
      clumsy for the team, decide on a login (single sign-on or per-user keys).
- [x] Audit log of drafts: one "report drafted" log line per draft with key
      fingerprint, program, period, documents read and cited. It is only a
      log; keep it somewhere searchable once hosting is chosen.
- [ ] A donation API that is down or rejects the key shows as "internal
      server error"; give it its own error code (502) so people know it is
      not their request.
- [ ] Mask donor names, not only contact details (`report.MaskPersonalData`
      handles emails, phone numbers and national IDs only).

## Pilot 2: funder shortlist and approach emails

Goal: from one Human Initiative program, a shortlist of companies that may
fund it, each with the reasons, the evidence and a draft approach email, for
a person to review. Built on the CSR index above. How it works:
[docs/outreach.md](docs/outreach.md).

The flow is built end to end and tested with a fake model and an in-memory
index: `POST /api/v1/outreach` (behind `OUTREACH_ENABLED`), `csrctl shortlist`
(no model needed, with a CSV rating sheet), emails checked against the
evidence they cite, per-run trace of tokens and estimated cost. What is left
needs real index data, a Gemini key and the program team. Same rule as pilot
1: facts come from the index and its evidence, the model only words the email.

### Needs a crawled index (a Gemini key and SearXNG)

- [ ] The index is only as good as what has been crawled: do the real
      extraction and discovery items under "Follow-ups to Phase 2.5" and
      "Phase 3 follow-ups" first. `csrctl shortlist` then shows what exists
      for a program; with a thin index, `fitting` will be small.
- [ ] Decide `CSR_REVIEWER_KEY_IDS` and review companies from the shortlist
      (`csrctl set-status`): most are `new`, and each entry says so.

### Needs the program team

- [ ] Real Human Initiative programs (name, description, focus areas, regions,
      types), 3 to 5 to start, written into `config/hi-programs.yaml`
      (start from `config/hi-programs.example.yaml`, set
      `OUTREACH_PROGRAMS_FILE`). Use the fields from
      `GET /api/v1/outreach/vocabulary`; a term outside them only matches the
      exact same words, and the response says so in `warnings`.
- [ ] Say which criteria beyond focus, region and type the team wants: the
      amount needed, timing, number of beneficiaries. None affects the score
      today.
- [ ] Rate relevance: run `csrctl shortlist -csv` for 3 or more real programs
      and have the Penanggung Jawab fill the sheet for about 30 companies.
      Target: at least 70% relevant. Tune `internal/websearch/csr/score.go`
      from the misses.
- [ ] Read 10 or more drafted emails with a real model and judge tone,
      language and length; adjust the prompt in
      `internal/application/prospecting/email.go`. Decide how emails are
      signed (today `[Nama pengirim]`, `[Jabatan]`, `[Kontak]`).
- [ ] Replace the example values in `config/institution-profile.yaml`: they
      still drive the chat tools (`find_csr_prospects`), though not the
      shortlist.

### Needs a Gemini key

- [ ] Check cost and time of a run of 10 companies with emails; set
      `MODEL_*_RUPIAH_PER_MILLION_TOKENS` to the real prices so the trace
      shows rupiah. The request timeout (default 60 s) must cover the slowest
      run: lower `limit` or raise `REQUEST_TIMEOUT_SECONDS` if needed.
- [ ] Look at how often an email is rejected twice (trace `outcome`) and how
      often the number check flags something; those rates say whether the
      prompt or the checks need work.

### Later

- [ ] A page for the program team like `/reports`: describe a program, see
      the shortlist with notes and evidence, edit and copy an email.
- [ ] Remember past shortlists and emails sent, so a company is not
      approached twice for the same program (needs a person to record what
      was sent).
- [ ] Chat tool that drafts an email for one company on request.

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
- [ ] Alert on crawl health: the harness logs "web metrics" every
      `WEB_METRICS_LOG_MINUTES` and every csrctl run prints its web summary
      (fetch ok/failed/blocked, search ok/failed); hook a log-based alert to
      a rising blocked or search-failed count once hosting is chosen.

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
