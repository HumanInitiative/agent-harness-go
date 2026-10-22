# Donation data API (contract)

What the report feature reads from Human Initiative's donation system: the
figures of a program over a period, and the program's activity documents.
The harness client is `internal/adapters/outbound/reportapi`; set
`REPORT_API_URL` (and `REPORT_API_KEY`) to use it.

The real API does not exist yet. This is the contract the client expects,
served today by a mock with invented data (`make mock-api`,
`internal/mockapi`). When the real API arrives, either it implements this
contract or the client is adjusted to it; nothing else in the harness
changes.

Authentication: an optional `X-API-Key` request header. Errors are JSON,
`{"error": "..."}`. The client never follows redirects.

## `GET /v1/programs/{id}/report-data?from=YYYY-MM-DD&to=YYYY-MM-DD`

Figures for the program, both dates inclusive.

```json
{
  "program": {"id": "pangan-jakarta", "name": "Bantuan Pangan Jakarta"},
  "period":  {"from": "2026-01-01", "to": "2026-03-31"},
  "as_of": "2026-07-01T08:00:00Z",
  "received":  [{"label": "Donatur individu", "rupiah": 135000000}],
  "disbursed": [{"label": "Paket pangan",     "rupiah": 177000000}],
  "beneficiaries": [{"group": "Keluarga", "people": 1320}]
}
```

- Amounts are whole rupiah (integers), never negative. `received` is grouped
  by source or month, `disbursed` by category; the labels are printed in the
  report as given.
- `beneficiaries` groups must not overlap, or the report's total counts
  people twice.
- `as_of` is when the data was read (RFC 3339); the report prints it.
- Empty lists are `[]`. Totals are not sent: the harness sums the lines, so
  a total cannot disagree with its table.
- `404` for an unknown program, `400` for a bad date or `to` before `from`.

## `GET /v1/programs/{id}/documents`

The program's activity documents.

```json
[{"name": "laporan-kegiatan-q1-2026.md", "content_type": "text/markdown", "size": 1953}]
```

`name` is a plain file name (no `/`), unique within the program. The
harness reads `.pdf`, `.docx`, `.txt` and `.md`, and ignores other
extensions. At most 50 documents are downloaded per report. `404` for an
unknown program (the harness treats it as no documents).

## `GET /v1/programs/{id}/documents/{name}`

The file itself (at most 50 MB), with its content type. `404` if the
program has no such document.

## `GET /v1/programs` (mock only)

`[{"id": "...", "name": "..."}]`, to see which programs the mock serves. The
harness does not call it; a later program picker will.

## The mock

`make mock-api` serves on `127.0.0.1:8090` (`-addr`, and `-key` or
`$MOCKAPI_KEY` to require a key). Two programs, January to June 2026:

| Program id | Documents |
|---|---|
| `pangan-jakarta` | `laporan-kegiatan-q1-2026.md`, `evaluasi-triwulan-ii-2026.txt`, `catatan-lapangan-maret-2026.docx` (generated) |
| `sekolah-desa` | `laporan-semester-1-2026.md` |

Figures are computed from dated transactions, so a different period gives
different totals. The Q1 documents of `pangan-jakarta` state the same
numbers as the Q1 figures (1,320 families, 2,030 children), so a good
narrative passes the number check. The documents include a contact name,
email and phone number to show the masking. There is no PDF fixture.

Try the whole flow:

```bash
make mock-api &                                   # the donation API
REPORT_TOOLS_ENABLED=true REPORT_API_URL=http://127.0.0.1:8090 \
  GEMINI_API_KEY=... API_KEYS=... make run        # the harness
curl -X POST localhost:8080/api/v1/reports -H "X-API-Key: $KEY" \
  -d '{"program_id":"pangan-jakarta","from":"2026-01-01","to":"2026-03-31"}'
```
