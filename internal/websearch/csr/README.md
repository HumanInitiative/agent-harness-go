# internal/websearch/csr

Finds companies that may fund social programs (CSR/TJSL) and keeps an
evidence-backed local index of them, for the `find_csr_prospects` and
`check_company` tools. Like its parent package it imports nothing from the
harness: fetching, searching and the LLM come in through small interfaces.

## Pipeline

```
seed CSV/JSON ──► companies ──► Resolver ──► company_pages (routing record)
                                   │               │
                       domain check, homepage,     │ Crawler: check due routes,
                       sitemap, search, probes     │ hash content
                                                   ▼
                         reports: SelectPages (rules) picks ~8 pages
                                                   ▼
                     ProfileExtractor (LLM) ── verify ──► evidence + csr_profile + csr_programs
                                                                              │
     find_csr_prospects / check_company ◄── Index (dates, lifecycle, FTS5) + InstitutionProfile.Score
```

| File | Responsibility |
|---|---|
| `store.go`, `schema_v*.sql` | SQLite (WAL) with FTS5 over evidence and programs; stepwise migrations via `PRAGMA user_version` |
| `programs.go` | Program records and their lifecycle (active, expired, stale, inactive) |
| `pages.go` | Rule-based selection of the CSR pages of long reports |
| `lock.go` | Named lock so two crawls never overlap |
| `seed.go` | CSV/JSON import, dedup by normalized name, per-row problem report |
| `normalize.go` | Name normalization, brand tokens/acronyms, canonical URLs |
| `classify.go` | Scores a link as a CSR page, and decides its kind |
| `resolve.go` | Confirms the domain, finds routes, records them |
| `sitemap.go` | Sitemap and sitemap-index parsing |
| `crawl.go` | Scheduled crawl: route checks, change detection, extraction, rescheduling, run limits |
| `extract.go` | LLM extraction with verification of every claim |
| `score.go` | Institution profile and explainable fit score |
| `query.go` | `Index`: `FindProspects`, `CheckCompany`, freshness, free-text search |

## The routing record (`company_pages`)

Every page known to describe a company's CSR work is recorded with its
kind (`csr_program`, `report`, `news`, `foundation`), **how it was found**
(`homepage`, `sitemap`, `search`, `probe`, `manual`), a link score, its
state, its last HTTP status and content hash, and when to check it again.

- Crawls go **straight to recorded routes**. Discovery runs only when a
  company has no usable route, including right after every route failed
  (a site redesign).
- **Rechecks by kind:** program pages weekly, foundation pages every two
  weeks, news every three days, reports every 90 days. Failures back off
  1 → 2 → 4 … 30 days.
- `gone` (404/410) and `blocked` (WAF, 403, robots.txt) routes are retried
  after 30 and 14 days respectively; a successful check makes them active
  again.
- **People override automation:** `csrctl route pin` records a page by
  hand, and `csrctl route reject` marks one as wrong. Automatic discovery
  never changes a pinned or rejected route.
- `domain_access` remembers whether each site is reachable (`ok`,
  `blocked`, `js_rendered`, `forbidden`, `unreachable`,
  `robots_disallowed`), so a blocked site is not hammered.

## Finding the right CSR page

A survey of the seed companies' homepages showed CSR paths vary far too
much to guess (`/pages/view/csr.html`, `/id/berita-csr`,
`/keberlanjutan-2/laporan-keberlanjutan/`,
`/program-allianz/corporate-social-responsibility.html`, `tjsl.kai.id`), so
`Resolver` tries strategies cheapest and most trustworthy first:

1. **Domain check.** Fetch the homepage (falling back to `www.` when the
   bare domain does not resolve). A domain that does not exist becomes
   `invalid`. A domain is `verified` when its host carries the company's
   brand token or acronym (`bri` in `bri.co.id`) or its homepage names the
   company. For companies without a domain, a search proposes a
   `candidate`.
2. **Homepage links**, scored by `ScoreLink`.
3. **Sitemaps** (from robots.txt, `/sitemap.xml`, `/sitemap_index.xml`).
   Child sitemaps most likely to list pages are read first.
4. **Search** (`site:domain CSR OR TJSL …` and `"Name" program CSR TJSL`).
   This is the main way into WAF-protected and JavaScript-only sites:
   search engines index pages we cannot fetch.
5. **Common paths** (`/csr`, `/tjsl`, …), only when nothing else worked.

A page on another domain is accepted only if its host carries the
company's brand (an investor-relations or foundation site such as
`ir-bri.com`), never a social network, news portal or directory.

`ScoreLink` weights CSR vocabulary in the link text, URL path and
subdomain, and **penalizes look-alikes that the survey actually found**:
"Media Sosial", "Tanggung Jawab Dewan Komisaris", "Tanggung Jawab Produk",
"Bidang Usaha … TJSL", "Supply Chain Due Diligence". Its test table is made
of those real links.

### Measured on the seed list (100 companies, 2026-10-01)

Discovery only (`csrctl crawl -discover-only`), with SearXNG and the
crawler's default pacing; the run took about 13 minutes:

| | Without pacing, 3 engines | With pacing, 5 engines |
|---|---|---|
| Search failures | upstream engines suspended within minutes | 0 |
| Companies with a CSR program/foundation route | 47 | 78 |
| Companies with any CSR route | 54 | 91 |
| Seed rows without a domain whose domain was found | 0 of 21 | 18 of 21 |

Routes found by search: 178; by sitemap: 121; from homepages: 127.
Off-domain routes found include company-run CSR sites (`kalbecsr.id`), IR
sites (`ir-bri.com`, `sidomuncul.listedcompany.com`) and a company's real
domain when the seed's was wrong (`beraucoalenergy.co.id`).

The same run exposed precision errors that are now prevented and tested:
a city government site and a sister company's site accepted as a company's
domain (search-found domains now need the homepage to name the company,
and government/academic domains are refused), and another bank's site
accepted because both names contain "syariah" (now a generic word;
off-domain routes from search also rank below the company's own pages).

## Reading long reports: rules choose, the model reads a little

Annual and sustainability reports are where many companies describe their
CSR programs, and they are long: two measured MIND ID reports are 85 and
101 MB, 264 and 380 pages, about 184k and 266k tokens of text. Sending
whole reports to a model on every crawl would cost far more than the rest
of the pipeline together, so the work is split:

1. **Download and convert** (no model): the PDF is streamed to a temporary
   file (`CSR_CRAWL_MAX_PDF_BYTES`, default 150 MB) and converted by
   `pdftotext`; the 85 MB report takes about 9 s including the download.
2. **Choose pages** (rules, `SelectPages`): lines repeated on many pages
   (running headers, footers, navigation bars) are removed first. Each
   page is then scored with a weighted CSR vocabulary in Indonesian and
   English (TJSL, penerima manfaat, beasiswa, mitra binaan, MSME,
   livelihood, amounts in Rupiah ... up; table of contents, financial
   statements, employee training hours, human rights, procurement ...
   down), counting repeats up to three times, so a program page that keeps
   returning to its subject beats a summary page naming many subjects
   once. A page also inherits 30% of its stronger neighbour's score,
   because reports describe programs in runs of pages and a page deep in a
   run often names only its own program. The best pages up to about 8k
   tokens are kept, in page order: **3-5% of a long report**.
3. **Extract** (model, once per version of the report): only the chosen
   pages are sent, each marked `[Halaman N]`. Evidence from a report links
   to its page (`report.pdf#page=229`), and an excerpt counts only if it is
   on a page the model was shown.
4. **Judge fit** (rules, `Score`): the institution's fit is never asked of
   a model, so asking many questions costs nothing.

Reports are rechecked every 90 days; an unchanged report (same content
hash) is never sent to the model again. A report without any page that
looks like CSR content is skipped without a model call.

### Measured page selection (2026-10-08)

Eight real reports, with the pages that describe community or CSR
programs marked by hand: banking (BCA, Bank Mandiri, BNI), food (Charoen
Pokphand), insurance (Asuransi Astra) and mining (Merdeka Gold, RAIN,
Medco), bilingual, Indonesian-only and English-only. Precision is the
share of selected pages that are program pages:

| Rules | Mean precision | English-only (Medco, BNI) |
|---|---|---|
| First version (presence of terms, mostly Indonesian vocabulary) | 0.67 | 0.80, 0.50 |
| Current (headers removed, capped repeats, neighbours, bilingual vocabulary) | 0.90 | 1.00, 0.90 |

The English-only reports were not used for tuning; they were added
afterwards to check that the rules carry over. Most remaining misses are
summary pages that do mention CSR programs (a materiality topic
"Pengembangan Masyarakat", a TPB contribution table).

`go test -tags live -run Live -timeout 30m ./internal/websearch/csr/`
repeats the measurement on the same reports (downloading about 200 MB)
and fails if mean precision drops below 0.8; run it after changing the
rules.

## Extraction you can trust

The LLM reads up to four changed pages (program pages first; reports as
selected pages), each wrapped as `<web_content untrusted="true">`, and
must cite evidence for every claim. Its answer is untrusted input:

- the shape is strict (unknown fields, a missing field or an out-of-range
  confidence trigger one corrective retry, then `ErrExtractionFailed`, and
  nothing is stored);
- **every excerpt must occur in the page it cites** (case, punctuation and
  markdown are ignored, but a paraphrase is not accepted), and must cite a
  URL that was actually given;
- every claim needs at least one verified excerpt, otherwise it is dropped
  and listed in `Extraction.Dropped`;
- a proposal channel must be an email address or URL on the company's own
  (or brand-carrying) domain: free-mail addresses, social media, phone
  numbers and people's names are refused;
- every program needs a verified excerpt, and **a date is kept only when a
  cited excerpt contains its year**: the model cannot invent a deadline;
- saving is transactional: claims are stored with the IDs of their
  evidence rows, or nothing is stored.

Unchanged pages (same content hash) are never sent to the LLM again. An
extraction replaces only what came from the pages it read: claims and
programs known from other pages are kept.

## Programs and their lifecycle

Each program is a record of its own (`csr_programs`): name, description,
focus areas, regions, types, period, proposal deadline, evidence, the
pages it came from, and when it was first and last seen. The same program
in different years ("Beasiswa 2023", "Beasiswa 2026") is two records.
Nothing is deleted; status moves instead:

| Status | When |
|---|---|
| `active` | seen in the latest extraction of its pages, dates not passed |
| `expired` | its end date or proposal deadline has passed (a date rule, applied when read and stored by each crawl run) |
| `stale` | its pages were re-read and it was missing once |
| `inactive` | missing from two consecutive re-reads |

A program that reappears becomes `active` again. A program is only
counted as missing when every page it came from was re-read.

## Answers: from the index, with dates

The tools never fetch anything; they answer from the index and show:

- **"Data per"**: when the model last read the company's pages, and when a
  page was last successfully checked (an unchanged page confirms the data
  without a model call);
- each evidence excerpt's URL and fetch date;
- programs with status, period and deadline; only active ones unless
  `include_inactive` is set;
- a **stale** warning, ranked last, when no page confirmed the data for
  `CSR_STALE_AFTER_DAYS` (default 120, above the 90-day report recheck) or
  every recorded page is gone or blocked.

`find_csr_prospects` also takes free text (`query`, e.g. "beasiswa
Banten"), matched with FTS5 against programs and against the evidence of
current claims: every meaningful word must match, otherwise any word.
Active programs in the institution's fields or regions add 10 points to
the fit score; a company whose programs have all ended loses 10.

## Scoring

`InstitutionProfile` (YAML, see `config/institution-profile.yaml`) lists
focus areas, regions and program types. `Score` sums explained parts:

- focus match: 25 for the first, plus 10 for each additional, at most 45;
- region proven by the pages: 20 (nationwide: 15; only the seed's label: 5);
- program type match: 10;
- the company invites proposals: 15;
- an official proposal channel exists: 10;
- an active program in the institution's fields or regions: 10 (all
  recorded programs ended: minus 10).

Synonyms connect the vocabulary of the company's pages to the institution's
profile ("beasiswa" → pendidikan, "UMKM" → pemberdayaan ekonomi, "Jabar" →
Jawa Barat). Every point comes with a reason, and a low extraction
confidence is flagged rather than hidden.

## Operations

```bash
csrctl import ../csr-seed-companies.csv     # idempotent; reports skipped rows
csrctl crawl -discover-only                 # routes only, no LLM quota
csrctl crawl                                # routes + extraction (GEMINI_API_KEY)
csrctl schedule -at 02:00 -tz Asia/Jakarta  # crawl daily until stopped
csrctl companies
csrctl show "Bank Rakyat Indonesia"         # routes, profile, programs, evidence, dates
csrctl route pin 12 https://example.co.id/tjsl
csrctl set-status 12 verified               # people verify; automation never does
```

Crawling runs outside the harness, daily: the `csr-crawler` service in
`deploy/docker-compose.yml` (`csrctl schedule`), a Kubernetes CronJob
(`deploy/kubernetes/csr-crawl-cronjob.yaml`) or host cron
(`deploy/cron/csr-crawl.cron`). Each run:

- takes a **lock** in the database: a second crawl (a manual run during
  the scheduled one) exits at once with a message naming the running one;
  a crashed crawl's lock expires after 15 minutes;
- stores the `expired` status of programs whose dates passed;
- handles at most `CSR_CRAWL_COMPANIES_PER_RUN` due companies (default
  100) and starts none after `CSR_CRAWL_MAX_RUN_MINUTES` (default 180);
  companies left over are reported and logged as a warning, which is the
  signal to raise the limit or run more often.

Crawls are deliberately slow (5 s between requests to one site, 6 s
between searches): a faster test crawl got the SearXNG instance's upstream
engines suspended and one company's WAF blocking the server's IP within
minutes. Route checks took about 8 s per company per worker in a measured
crawl; extraction and large report downloads add to that.
