# Donation reports

How the donation report draft is made, what it contains, and who writes
what. The code is in `internal/domain/report` (layout and checks),
`internal/application/reporting` (the flow) and the adapters named below;
this page is the agreement with the program team about the report. Change
the layout in the code and here together.

Status: the flow works end to end. The section list has not been confirmed
by the program team, and figures and documents come from a mock of the
donation API until the real one exists (see TODO.md, "Pilot 1").

## The rule

Every number in the figure sections is filled in by code from the donation
data. The model never writes, rounds or recalls them. The model writes only
the two narrative sections, and only from the program's activity reports. A
draft is a draft: a person reviews it before it is sent.

## Flow

```
POST /api/v1/reports  {program_id, from, to, documents?, format?}
   |
   |-- ReportDataSource ---------> figures (Rupiah, people)        no model
   |        donation data API (REPORT_API_URL), or sample figures
   |-- ActivityDocuments --------> text of the program's reports   no model
   |        the same API, or a folder
   |        pdf (pdftotext), docx, txt, md
   |        contact details masked, long documents cut
   |-- model, one call ----------> JSON: {activities, challenges}, each with
   |        retried once if the      text and the documents it cites
   |        answer is unusable
   |-- checks -------------------> every cited source must be a document given;
   |                               every number not in the figures or the cited
   |                               documents becomes a reviewer note
   `-- Assemble + render --------> draft: JSON (Markdown) or .docx
```

What stops a bad draft from looking good:

| Problem | What happens |
|---|---|
| Program unknown | 404 `program_not_found`, no model call |
| No readable activity documents | 422 `no_documents`, no model call |
| Model answers badly (not JSON, missing section, cites a file it was not given) | One retry telling it why; then 502 `narrative_failed` |
| Narrative contains a number nobody stated | Draft is produced, with a "Notes for the reviewer" section naming the numbers |
| A document is cut, unreadable or has no text (scanned PDF) | Draft is produced, with a note naming the file |
| Document tries to give the model instructions | Its text is only data inside `<document>` tags; the closing tag is neutralised |

The reviewer notes are in the draft itself and in the `notes` field of the
JSON response.

## Data and documents

With `REPORT_API_URL` set, the harness reads figures and documents from the
donation data API; its contract, and the mock that stands in for it until the
real one exists, are in [donation-data-api.md](donation-data-api.md).

Without it (no API yet), figures are the built-in sample and the activity
documents are read from a folder. Put each program's reports in a folder
named after the program id:

```
$REPORT_DOCS_DIR/            (default data/report-docs)
  <program id>/
    laporan-q1.pdf
    catatan-lapangan.docx
```

Supported: `.pdf` (needs `pdftotext` from poppler-utils), `.docx`, `.txt`,
`.md`. All files of the program are used unless the request names some in
`documents`. At most 10 documents and 120,000 characters go to the model per
report, 30,000 per document. Emails, mobile numbers and 16-digit ID numbers
are replaced with placeholders before the text goes to the model; names are
not masked.

## Using it

```bash
make mock-api &                      # invented donation API (docs/donation-data-api.md)
REPORT_TOOLS_ENABLED=true REPORT_API_URL=http://127.0.0.1:8090 make run
curl -X POST localhost:8080/api/v1/reports \
  -H "X-API-Key: $KEY" \
  -d '{"program_id":"pangan-jakarta","from":"2026-01-01","to":"2026-03-31"}'
# add "format":"docx" and -o report.docx to get the Word file
```

`REPORT_TOOLS_ENABLED` also registers the `get_report_figures` chat tool.
Without `REPORT_API_URL` the only program is `sample-program`. See the
Swagger UI for the full request and error list.

## Sections

| # | Section | Written by | Content |
|---|---------|------------|---------|
| - | Header | code | Program, period, "DRAFT" notice, "Reviewed by / Date" line |
| - | Notes for the reviewer | code | Only when the checks found something |
| 1 | Summary | code | Funds received, funds disbursed, balance, beneficiaries |
| 2 | Funds received | code | Table by month or source, with total |
| 3 | Funds disbursed | code | Table by category, with total |
| 4 | Beneficiaries | code | Table by group, with total |
| 5 | Activities and impact | model | Narrative from the activity reports, sources listed |
| 6 | Challenges and next steps | model | Narrative from the activity reports, sources listed |
| - | Appendix | code | Data timestamp, list of source documents |

Totals and the balance are computed from the table lines, so a total never
disagrees with the table above it. Amounts are whole rupiah, written
`Rp 1.250.000`. The balance can be negative when more was disbursed than was
received in the period. Beneficiary groups must not overlap, or the total
counts people twice; the data source is responsible for that.

## Example (sample data)

```markdown
# Donation report: Sample program (invented data)

Period: 2026-01-01 to 2026-03-31

**DRAFT. Written with AI assistance from the donation data and activity
reports. Review before sending.**

Reviewed by: ____________  Date: ____________

## 1. Summary

| Measure | Value |
|---|---:|
| Funds received | Rp 250.000.000 |
| Funds disbursed | Rp 150.000.000 |
| Balance | Rp 100.000.000 |
| Beneficiaries | 860 |

## 2. Funds received

| Month or source | Amount |
|---|---:|
| Individual donors | Rp 150.000.000 |
| Corporate donors | Rp 100.000.000 |
| **Total** | **Rp 250.000.000** |

## 5. Activities and impact

<narrative from the activity reports>

Sources: laporan-q1.pdf
```

Sections 3, 4 and 6 follow the same pattern. The full layout is produced by
`report.Assemble` and covered by `internal/domain/report/report_test.go`.

## Not decided yet

- Whether the program team wants the sections above, in this order, and in
  English or Indonesian headings.
- Which recipient the tone targets (individual donor, corporate donor,
  internal).
- How activity documents should reach the agent in production (folder,
  upload, document store).
- PDF output (only `.docx` and Markdown exist).
