# Funder shortlist and approach emails (pilot 2)

From one Human Initiative program to the companies that may fund it: a
shortlist from the CSR index, the reasons each fits, the evidence behind
them, and a draft approach email per company for a person to read, adjust and
send. Nothing is ever sent automatically.

Code: `internal/domain/outreach` (types, email checks, layout),
`internal/application/prospecting` (the flow),
`internal/adapters/outbound/csrprospects` (scoring against a program),
`internal/adapters/inbound/httpapi/outreach_handler.go` (the endpoint).

## The rule

Which companies, and why, comes from the evidence index and its explainable
score. Facts about a company come from evidence excerpts that were verified
to appear on the company's own pages. The model only words the email, and
every claim in it must cite evidence the index holds. What the checks cannot
confirm becomes a note for the reviewer instead of being trusted.

## Flow

```
POST /api/v1/outreach  {program, limit?, emails?, format?}
   |
   |-- csrprospects ---------> score every indexed company against THIS program
   |        focus area must match (region or a proposal channel alone is not a fit);
   |        current data before stale, trusted before doubtful, then score
   |-- notes from the index -> unverified, stale data, low confidence, no proposal
   |                           channel, "not seeking partners", no active program
   |-- model, one call per company (4 at a time, no model call when emails=false)
   |        JSON {subject, body, cited_evidence}; retried once with the reason
   |-- checks ---------------> cited evidence must be what the model was shown;
   |                           an email address or link found in no evidence is
   |                           rejected (retry); a number found in no evidence
   |                           (dates of the evidence count) becomes a note
   `-- shortlist + trace ----> JSON, or a .docx with the email and its basis
```

A company whose email cannot be produced keeps its place in the shortlist
with a note. The run fails only when the model is unavailable or the caller
has gone, because then no other email can succeed either.

## What the reviewer is told

| Situation | Note on the entry |
|---|---|
| Company record not confirmed by a person | belum diverifikasi orang |
| No page confirmed the data recently, or every page is gone or blocked | data sudah lama, with the reason; the email is told not to present it as current |
| Extraction confidence below the threshold | keyakinan ekstraksi rendah |
| No proposal channel known | cari tahu cara menghubungi perusahaan |
| Pages say the company is not open to partners | explicit note |
| No active CSR program recorded | explicit note |
| A number in the email is in no cited evidence or program text | the numbers are listed |
| The model could not give an acceptable email in two tries | "Draf email gagal dibuat" with the reason |

Each email is shown with the excerpts it cites (title, date, URL, text), so
the reviewer can check a claim against its source in one glance. Contact
details the email may use are only those in the evidence or in the company's
proposal channel; the sender's name, title and contact are left as
`[Nama pengirim]`, `[Jabatan]`, `[Kontak]`.

## Cost and trace

Every run returns a `trace`: how many companies fit before the cut, tokens
in and out per email and in total, attempts, outcome and duration. Set
`MODEL_INPUT_RUPIAH_PER_MILLION_TOKENS` and
`MODEL_OUTPUT_RUPIAH_PER_MILLION_TOKENS` to also get an estimated cost in
rupiah (without them the trace has tokens and no cost). The same totals go in
one log line, "shortlist built", with the key fingerprint of the caller; the
text of the emails never goes to the log. Donation reports log their tokens
the same way ("report drafted").

Token counts are what the model provider reports for each call. They are
exact here because every call is a single turn without tools.

## Using it

```bash
# needs the CSR index (csrctl import / crawl) and a Gemini key
OUTREACH_ENABLED=true CSR_TOOLS_ENABLED=true make run

curl -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $KEY" -d '{
  "program": {"name": "Beasiswa Anak Desa", "focus_areas": ["pendidikan"],
              "regions": ["Jawa Barat"], "program_types": ["beasiswa"],
              "description": "Beasiswa untuk 120 anak di desa terpencil."},
  "limit": 10}'
# "emails": false for the shortlist alone (no model call), "format": "docx" for Word
```

## Looking at the shortlist without a model

`csrctl shortlist` ranks the indexed companies against a program file
(`config/program.example.yaml`) using only the index, so it needs no Gemini
key and costs nothing:

```bash
csrctl shortlist -program config/program.example.yaml            # a table
csrctl shortlist -program config/program.example.yaml -csv > sheet.csv
```

Use it to see what the index can offer for a program before spending model
calls, and to tune the scoring (`internal/websearch/csr/score.go`).

## Measuring relevance

The pilot's target is that the Penanggung Jawab rates at least 70% of
shortlisted companies relevant. The CSV from `csrctl shortlist -csv` is the
rating sheet: a person fills the last two columns (`relevant (y/n)` and
`comment`) for 30 companies across several programs, and relevance is the
share of `y`. Keep the filled sheets: they are the evidence for tuning the
score weights and the first eval set. A low result with a small index means
the index does not cover the program's field yet, not that scoring is wrong:
check `fitting` (companies that fit before the cut) first.

## What it does not do

- It does not crawl. The index is filled by `csrctl crawl` and discovery;
  until they have run for real, results are only as good as the seed data.
- It does not send anything, and it does not know who the sender is.
- Scoring uses the program's focus areas, regions and types; the
  institution-wide profile in `config/institution-profile.yaml` no longer
  drives the shortlist, only the chat tools.
- Each request scores up to 1,000 indexed companies. That is fast for the
  current index; revisit if the index grows well beyond it.
