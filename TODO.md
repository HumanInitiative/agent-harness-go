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

## Pilot 1: donation reporting agent (skeleton)

Goal: a program team picks a program and a period and gets a draft report
to review. Figures come from an API endpoint; the narrative sections are
written by the model from the activity reports (docx or pdf) of that
program. Project-level plan and owners:
the shared TODO document ("TODO Proyek Agentic AI & DevOps").

This stage only lays out the skeleton and the document template so the
rest of the work can be sized. No real data and no model calls yet.

How the pieces fit:

```
program + period
   |-- report data client --> API endpoint --> typed figures (JSON)
   |-- activity documents (docx / pdf) --> text --> model --> narrative
   `-- template: fixed layout, figures filled by code, narrative slots
                          |
                          v
                 draft DOCX / PDF  -->  human review
```

Rule that shapes everything: **figures are filled by code from the
endpoint response, never written by the model**. The model only writes the
narrative slots, and only from the supplied documents.

### Decide first

- [ ] Get the endpoint contract from whoever owns the donation API: URL,
      auth, parameters (program, period), response fields, and one sample
      response per program. Without it the template fields below are guesses.
- [ ] Decide how activity documents reach the agent: uploaded with the
      request, a folder or storage path per program and period, or an
      endpoint that lists them. Decide the maximum size and count.
- [ ] Decide the template format: a `.docx` with placeholders (editable by
      the program team, rendered by filling it) or Markdown rendered to
      DOCX/PDF. This also decides whether the render step is Go or a
      Python sidecar (an ADR is already planned for the sidecar).
- [ ] Decide who receives the report (individual donor, corporate donor,
      internal), since it sets tone and which sections are required.

**Done when:** the four decisions are written down (an ADR or a short
note in `docs/`) and the sample responses are in the repo as fixtures.

### Document template and sections

- [ ] Write the section list with the program team and fix it in
      `docs/donation-report-template.md`. Starting proposal:
      1. Cover: program, period, report date, status "DRAFT".
      2. Summary: funds received, funds disbursed, balance, beneficiaries
         (figures, filled by code).
      3. Funds received: table by month or source (figures).
      4. Funds disbursed: table by category (figures).
      5. Beneficiaries: count by group and region (figures).
      6. Activities and impact: narrative written by the model from the
         activity documents, with the source document named per paragraph.
      7. Challenges and next steps: narrative, same rule.
      8. Appendix: list of source documents and the data timestamp.
- [ ] Mark each section as `figures` (code only) or `narrative` (model),
      and list the placeholder names (`{{total_received}}`, ...).
- [ ] Build the template file with placeholders and sample data, so a
      filled example can be opened and judged before any code exists.
- [ ] Put a visible "AI-generated draft, review before sending" note in the
      template, and an empty "Reviewed by / date" line.

**Done when:** a program team member has looked at the filled sample and
agreed the layout and sections.

### Code skeleton

- [ ] Outbound port `ReportDataSource` (get figures for program + period)
      with an HTTP adapter under `internal/adapters/outbound/`, behind a
      base URL and key from config; a fake for tests. The application layer
      must not import the HTTP client (see the dependency rule in
      CONTRIBUTING.md).
- [ ] Outbound port for reading activity documents and a text extractor:
      pdf through the existing `pdftotext` path, docx extracted without
      shelling out if a small Go library does it (decide in the PR).
- [ ] Tool `get_report_figures` returning the typed figures, registered in
      `registeredTools` in `cmd/harness/main.go` behind a new
      `REPORT_TOOLS_ENABLED` flag, with unit tests ("Adding a tool" in
      README.md).
- [ ] Tool `read_activity_documents` returning bounded text per document
      with the file name, so the model can cite it. Long documents are cut
      to a token budget, as the CSR report reader does.
- [ ] Report assembler (plain code, not a tool): takes figures plus the
      model's narrative slots and fills the template. Fails if a figure
      placeholder is empty or a narrative slot has no source cited.
- [ ] Render step (`/render` sidecar or in-process, per the ADR): filled
      template to DOCX and PDF. Contract written as OpenAPI first if it is
      a sidecar.
- [ ] Endpoint or agent entry point: `POST` with program and period,
      returns the draft file. Add to Swagger.

**Done when:** one request with fake data and a fake model returns a DOCX
that matches the sample template, and `make ci` is green.

### Narrative guardrails

- [ ] Prompt for the narrative slots: use only the supplied documents, say
      "not stated in the documents" instead of filling gaps, no figures in
      narrative unless they appear in the figures payload.
- [ ] Check after generation: every number in a narrative slot appears in
      the figures or in a cited document; otherwise flag the slot.
- [ ] Mask donor personal data before anything is sent to the model; the
      narrative never needs donor names.
- [ ] Eval set: 20 to 30 cases from past reports (program, period, expected
      figures, a reference narrative). Figures must match 100%.

**Done when:** the eval runs with a real Gemini key on at least five past
reports and the failures are listed and reviewed by a person.


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
