# Praktik: mencoba kedua pilot langkah demi langkah

Tutorial ini mengajak kamu **menjalankan** kedua pilot di laptop sendiri,
**tanpa kunci Gemini dan tanpa jaringan**, lalu melihat apa yang terjadi di
kodenya. Setiap langkah punya perintah, hasil yang diharapkan (diambil dari
run sungguhan), dan penunjuk ke fungsi yang bekerja di baliknya.

Baca sambil membuka [panduan umum](README.md). Penjelasan alur dan diagram
ada di [pilot 1](pilot-1-laporan-donasi.md) dan
[pilot 2](pilot-2-shortlist-pendana.md); halaman ini untuk **mencoba**-nya.

> **Tentang model palsu.** Demo ini memakai model palsu yang menulis dengan
> mengutip dokumen dan bukti yang ia terima, dan semua tulisannya diawali
> `[DEMO]`. Ia ada supaya seluruh alur bisa dijalankan tanpa kunci. Ia **tidak**
> menunjukkan mutu tulisan Gemini; itu baru bisa dinilai dengan kunci asli
> (lihat [Bagian 4](#bagian-4-menjalankan-dengan-gemini-asli)).

- [Bagian 0: persiapan](#bagian-0-persiapan)
- [Bagian 1: menyalakan demo](#bagian-1-menyalakan-demo)
- [Bagian 2: pilot 1, laporan donasi](#bagian-2-pilot-1-laporan-donasi)
- [Bagian 3: pilot 2, shortlist pendana](#bagian-3-pilot-2-shortlist-pendana)
- [Bagian 4: menjalankan dengan Gemini asli](#bagian-4-menjalankan-dengan-gemini-asli)
- [Bagian 5: uji pemahamanmu](#bagian-5-uji-pemahamanmu)
- [Bila ada masalah](#bila-ada-masalah)

Perkiraan waktu: 30 menit untuk tiap pilot, termasuk membaca fungsi yang
ditunjuk.

---

## Bagian 0: persiapan

Yang dibutuhkan: Go (versi di `go.mod`), `make`, `curl`, dan `python3` untuk
merapikan JSON. Browser untuk langkah halaman web. `jq` boleh dipakai sebagai
pengganti `python3 -m json.tool`.

```bash
git clone https://github.com/HumanInitiative/agent-harness-go.git
cd agent-harness-go

# Pastikan semuanya sehat sebelum mulai (beberapa menit pertama kali)
go test ./cmd/pilotdemo/                 # test seluruh demo, lewat HTTP sungguhan
```

Harapan: `ok  .../cmd/pilotdemo`. Bila gagal, hentikan di sini dan lihat
[bila ada masalah](#bila-ada-masalah).

Siapkan **dua terminal** di folder repo. Terminal 1 menjalankan demo dan
menampilkan lognya; terminal 2 untuk perintah `curl`.

## Bagian 1: menyalakan demo

**Terminal 1:**

```bash
make demo
```

Harapan:

```text
==============================  DEMO  ==============================
 Model PALSU, data karangan. Tidak menghubungi Gemini. Jangan dipakai
 untuk pekerjaan nyata.

 Harness      http://127.0.0.1:8080     (Swagger: /swagger/index.html)
 Halaman      http://127.0.0.1:8080/reports
 API donasi   http://127.0.0.1:8090            (tiruan)
 X-API-Key    demo-key-only-for-learning-0000000000
 Indeks CSR   data/demo/csr.db   (6 perusahaan karangan)
 Katalog      config/hi-programs.example.yaml
 Quirk model  (tidak ada)
 Langkah      docs/pilots/praktik.md
=====================================================================
```

Satu proses ini menyalakan semua yang dibutuhkan kedua pilot:

| Komponen | Apa | Kode |
|---|---|---|
| API donasi tiruan di `:8090` | Angka dan dokumen kegiatan karangan | `internal/mockapi` |
| Indeks CSR | SQLite berisi 6 perusahaan karangan, dibuat ulang setiap start | `cmd/pilotdemo/seed.go` |
| Model palsu | Menulis `[DEMO]`, melaporkan token | `cmd/pilotdemo/model.go` |
| Harness di `:8080` | **Kode yang sama dengan produksi**: router, handler, service | `cmd/pilotdemo/main.go` (`build`) |

Hanya model dan sumber datanya yang palsu. Handler, use case, pemeriksaan, dan
renderer adalah kode asli yang akan berjalan di produksi.

**Terminal 2:** siapkan kunci (sama dengan yang tercetak di banner):

```bash
export K=demo-key-only-for-learning-0000000000
curl -s localhost:8080/api/v1/programs -H "X-API-Key: $K"
```

Harapan:

```json
[{"id":"pangan-jakarta","name":"Bantuan Pangan Jakarta"},{"id":"sekolah-desa","name":"Sekolah untuk Desa"}]
```

Bila muncul `{"code":"unauthorized",...}`, kunci salah. Buka juga Swagger di
`http://127.0.0.1:8080/swagger/index.html` untuk melihat semua endpoint.

Menghentikan demo: `Ctrl+C` di terminal 1. Setiap kali demo dinyalakan,
indeks dibuat ulang, jadi kamu selalu mulai dari keadaan yang sama.

---

## Bagian 2: pilot 1, laporan donasi

Alur lengkapnya ada di [pilot 1](pilot-1-laporan-donasi.md#alur-satu-request).

### Langkah 2.1: lihat data mentah dari API donasi

Pilot 1 mengambil angka dan dokumen dari sebuah API. Lihat dulu apa yang
diberikan API itu.

```bash
curl -s "localhost:8090/v1/programs/pangan-jakarta/report-data?from=2026-01-01&to=2026-03-31" | python3 -m json.tool
```

Harapan (dipersingkat; `as_of` akan berbeda):

```json
{
    "program": {"id": "pangan-jakarta", "name": "Bantuan Pangan Jakarta"},
    "period": {"from": "2026-01-01", "to": "2026-03-31"},
    "as_of": "2026-10-22T02:05:47Z",
    "received": [
        {"label": "Donatur individu", "rupiah": 135000000},
        {"label": "Donatur korporat", "rupiah": 90000000},
        {"label": "Kampanye daring", "rupiah": 15000000}
    ],
    "disbursed": [ ... ],
    "beneficiaries": [ ... ]
}
```

Perhatikan: **tidak ada total**. API hanya mengirim baris. Totalnya dihitung
kode kita (`Figures.TotalReceived` di `internal/domain/report/report.go`),
supaya total tidak mungkin berbeda dari tabelnya. Handler yang melayani
ini: `Server.reportData` di `internal/mockapi/mockapi.go`.

**Coba:** ganti periode jadi satu bulan (`from=2026-02-01&to=2026-02-28`).
Datanya berisi transaksi bertanggal Januari sampai Juni 2026; periode
menentukan mana yang dijumlahkan.

### Langkah 2.2: lihat dokumen kegiatan

```bash
curl -s localhost:8090/v1/programs/pangan-jakarta/documents | python3 -m json.tool
```

Harapan: tiga dokumen.

```text
catatan-lapangan-maret-2026.docx    (Word, dibuat saat server menyala)
evaluasi-triwulan-ii-2026.txt
laporan-kegiatan-q1-2026.md
```

```bash
curl -s localhost:8090/v1/programs/pangan-jakarta/documents/laporan-kegiatan-q1-2026.md | tail -8
```

Di akhir dokumen ada nama dan kontak koordinator lapangan:

```text
Koordinator lapangan: Sari Wulandari (sari.wulandari@example.org, 0812-5550-1234).
```

Ingat ini: di langkah 2.8 kamu akan memeriksa bahwa email dan nomor HP itu
**tidak sampai ke model**.

### Langkah 2.3: buat laporan pertamamu

```bash
curl -s -X POST localhost:8080/api/v1/reports -H "X-API-Key: $K" \
  -d '{"program_id":"pangan-jakarta","from":"2026-01-01","to":"2026-03-31"}' \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['markdown']); print('NOTES:', d['notes']); print('SOURCES:', d['sources'])"
```

Harapan (bagian penting):

```markdown
# Donation report: Bantuan Pangan Jakarta

Period: 2026-01-01 to 2026-03-31

## 1. Summary

| Measure | Value |
|---|---:|
| Funds received | Rp 240.000.000 |
| Funds disbursed | Rp 221.000.000 |
| Balance | Rp 19.000.000 |
| Beneficiaries | 3.350 |

## 2. Funds received
...
| **Total** | **Rp 240.000.000** |

## 5. Activities and impact

[DEMO] Distribusi Maret dilakukan lebih awal menjelang Ramadan. Di 12 titik, relawan membagikan paket kepada 520 keluarga dan 780 anak, jumlah terbanyak pada triwulan pertama.

Sources: catatan-lapangan-maret-2026.docx

## 6. Challenges and next steps

[DEMO] Pada Februari, banjir merendam jalan masuk ke Rawa Badak selama empat hari. ...

NOTES: []
SOURCES: ['catatan-lapangan-maret-2026.docx', 'laporan-kegiatan-q1-2026.md']
```

Yang harus kamu perhatikan:

1. **Bagian 1 sampai 4 (angka) dan Appendix ditulis kode**; bagian 5 dan 6
   (narasi) ditulis model. Cocokkan dengan [diagram siapa menulis apa](pilot-1-laporan-donasi.md#siapa-menulis-apa).
2. Hitung sendiri: 135 + 90 + 15 = 240 juta. Itu cocok dengan Total, dan
   saldo 240 − 221 = 19 juta. Semua dihitung `report.Assemble`.
3. Setiap narasi menyebut **sumbernya**; tanpa sumber, `Assemble` menolak.
4. `NOTES` kosong: semua angka di narasi (520, 780, 12) memang ada di
   dokumen yang dikutip.

Fungsi yang baru saja bekerja, berurutan: `ReportHandler.ServeHTTP` →
`reporting.Service.Generate` → `reportapi.Client.Figures` →
`reportapi.Client.List` → `Service.prepare` → `Service.writeNarratives` →
`checkNumbers` → `report.Assemble`. Buka `Generate` di
`internal/application/reporting/service.go` dan ikuti baris demi baris; urutan
kodenya sama dengan urutan itu.

### Langkah 2.4: ubah periode, angka ikut berubah

```bash
curl -s -X POST localhost:8080/api/v1/reports -H "X-API-Key: $K" \
  -d '{"program_id":"pangan-jakarta","from":"2026-02-01","to":"2026-02-28"}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['markdown'][:700])"
```

Harapan: `Funds received | Rp 55.000.000`, `Funds disbursed | Rp 66.000.000`,
dan **`Balance | -Rp 11.000.000`**. Saldo negatif ditampilkan apa adanya: pada
Februari dana yang keluar lebih besar daripada yang masuk. Itu disengaja
(lihat komentar `Figures.Balance`).

### Langkah 2.5: unduh sebagai Word

```bash
curl -s -X POST localhost:8080/api/v1/reports -H "X-API-Key: $K" \
  -d '{"program_id":"pangan-jakarta","from":"2026-01-01","to":"2026-03-31","format":"docx"}' \
  -o laporan.docx -D -
open laporan.docx        # macOS; di Linux: xdg-open, atau buka dengan LibreOffice
unzip -l laporan.docx    # sebuah .docx hanyalah zip berisi XML
```

Harapan: header `Content-Disposition: attachment;
filename="report-pangan-jakarta-2026-01-01-2026-03-31.docx"`, dan `unzip -l`
menampilkan lima berkas (`word/document.xml`, `word/styles.xml`, ...).

Itulah `reportdocx.Renderer.Render`: tidak ada library dokumen, hanya
`archive/zip`. `report.Draft` yang sama dirender sebagai Markdown di langkah 2.3
dan sebagai Word di sini.

### Langkah 2.6: pakai halaman web

Buka `http://127.0.0.1:8080/reports` di browser.

1. Tempel kunci `demo-key-only-for-learning-0000000000`, tekan Enter.
   Daftar program terisi (dua program).
2. Pilih **Bantuan Pangan Jakarta**.
3. **Ganti tanggal** menjadi `2026-01-01` sampai `2026-03-31`.
   Periode bawaan halaman adalah kuartal sebelumnya (Jul–Sep), dan API tiruan
   hanya punya data Januari–Juni, jadi tanpa mengganti tanggal angkanya Rp 0.
4. Klik **Buat draf**. Status: "Draf siap. Tetap perlu direview sebelum
   dikirim." Kotak catatan reviewer tidak muncul karena tidak ada catatan.
5. Klik **Unduh Word (.docx)**.

Kode di balik halaman: `reportpage/app.js` (`loadPrograms`, `preview`,
`download`, `call`), disajikan oleh `ReportPage`. Daftar program datang dari
`GET /api/v1/programs` (`ReportHandler.Programs`).

Buka Developer Tools (tab Network) dan lihat dua permintaan yang dibuat
halaman: `GET /api/v1/programs` dan `POST /api/v1/reports`, keduanya dengan
header `X-API-Key`. Halaman itu sendiri tidak butuh kunci; panggilannya yang
butuh.

### Langkah 2.7: sengaja membuat error

```bash
# a. tanpa kunci
curl -s localhost:8080/api/v1/reports -X POST -d '{}'
# b. program tidak ada
curl -s -X POST localhost:8080/api/v1/reports -H "X-API-Key: $K" -d '{"program_id":"tidak-ada","from":"2026-01-01","to":"2026-03-31"}'
# c. tanggal terbalik
curl -s -X POST localhost:8080/api/v1/reports -H "X-API-Key: $K" -d '{"program_id":"pangan-jakarta","from":"2026-04-01","to":"2026-03-31"}'
# d. format yang tidak ada
curl -s -X POST localhost:8080/api/v1/reports -H "X-API-Key: $K" -d '{"program_id":"pangan-jakarta","from":"2026-01-01","to":"2026-03-31","format":"odt"}'
```

Harapan:

| | Status | `code` | Dari mana |
|---|---|---|---|
| a | 401 | `unauthorized` | `APIKeyAuth` (middleware) |
| b | 404 | `program_not_found` | API donasi 404 → `reportapi.Client.Figures` → `outbound.ErrProgramNotFound` → `writeReportError` |
| c | 400 | `invalid_request` | `report.Period.Validate` → `ErrInvalidInput` |
| d | 400 | `invalid_request` | `ReportHandler.ServeHTTP`, **sebelum** ada panggilan ke API donasi atau model |

Setiap error membawa `request_id`. Cari ID itu di log terminal 1: semua baris
untuk satu request punya ID yang sama.

### Langkah 2.8: baca log dan buktikan masking

Di terminal 1, cari baris ini (dipersingkat):

```text
level=INFO msg="report drafted" key_id=bd76fc17395a program=pangan-jakarta
  from=2026-01-01 to=2026-03-31
  documents_read="[catatan-lapangan-maret-2026.docx evaluasi-triwulan-ii-2026.txt laporan-kegiatan-q1-2026.md]"
  documents_cited="[catatan-lapangan-maret-2026.docx laporan-kegiatan-q1-2026.md]"
  review_notes=0 input_tokens=... output_tokens=...
```

Itu **jejak audit**: siapa (sidik jari kunci, bukan kunci), apa, dan dokumen
mana. Teks laporan dan dokumen tidak pernah masuk log.

Sekarang buktikan masking. Kontak koordinator (langkah 2.2) harus berubah
menjadi placeholder sebelum sampai ke model. Cara termudah: jalankan test yang
memeriksanya.

```bash
go test ./internal/application/reporting/ -run TestGenerate_KeepsContactDetailsAndInjectionAwayFromTheModel -v
```

Harapan: `--- PASS`. Buka test itu dan lihat bagaimana ia menangkap prompt yang
diterima model lalu memastikan email dan nomor HP tidak ada di sana. Yang
bekerja: `Service.prepare` → `report.MaskPersonalData`.

### Langkah 2.9: lihat pemeriksaan angka bekerja

Sejauh ini model palsu hanya mengutip, jadi tidak ada yang mencurigakan. Sekarang
buat model "berulah": ia menambahkan angka yang tidak ada di dokumen mana pun.

**Terminal 1:** hentikan demo (`Ctrl+C`), lalu:

```bash
make demo QUIRK=invent-number
```

**Terminal 2:** ulangi langkah 2.3 (perintah yang sama).

Harapan: `NOTES` tidak lagi kosong.

```text
NOTES: ['In the "activities" section, these numbers are in neither the figures nor the cited documents: 900. Check them.']
```

dan narasi bagian 5 kini berakhir dengan "Sebanyak 900 relawan terlibat
sepanjang periode ini." Di draf Markdown, catatan itu juga muncul sebagai
bagian **"Notes for the reviewer"** di atas Ringkasan.

Ini inti pilot 1: model **boleh salah**, tetapi kode tidak mempercayainya dan
menandai angkanya untuk manusia. Fungsinya: `checkNumbers` →
`report.NumbersIn` / `report.UnsupportedNumbers` /
`Figures.KnownNumbers`. Buka `TestGenerate_FlagsNumbersNobodyStated` untuk
versi test-nya.

### Langkah 2.10: lihat percobaan ulang

```bash
# terminal 1: Ctrl+C, lalu
make demo QUIRK=bad-json-once
```

Ulangi langkah 2.3. Laporan **tetap berhasil**, tetapi di log terminal 1
muncul:

```text
level=WARN msg="narrative rejected" attempt=1 reason="no JSON object found"
```

Percobaan pertama model palsu memang bukan JSON; `writeNarratives` menolaknya,
mengirim kembali alasannya, dan percobaan kedua berhasil. Ini loop di
[diagram "menulis narasi"](pilot-1-laporan-donasi.md#bagian-dalam-menulis-narasi).
Lalu `input_tokens` di baris `report drafted` kira-kira dua kali lipat: dua
panggilan model.

### Langkah 2.11: baca kodenya sekarang

Kamu sudah melihat perilakunya; sekarang kodenya akan terbaca jauh lebih mudah.
Urutan yang disarankan, masing-masing 10 sampai 15 menit:

1. `internal/application/reporting/service.go`: `Generate`.
2. `internal/application/reporting/narrative.go`: `writeNarratives`, `parseNarratives`.
3. `internal/domain/report/draft.go`: `Assemble`.
4. `internal/domain/report/document.go`: `NumbersIn`, `UnsupportedNumbers`.
5. `internal/application/reporting/service_test.go`: baca nama tiap test
   sebagai daftar perilaku yang dijamin.

Lanjut ke latihan di akhir [panduan pilot 1](pilot-1-laporan-donasi.md#latihan-ubah-sesuatu).

---

## Bagian 3: pilot 2, shortlist pendana

Alur lengkapnya ada di [pilot 2](pilot-2-shortlist-pendana.md#bagian-b-dari-program-ke-shortlist).
Pastikan demo berjalan **tanpa quirk** (`Ctrl+C`, lalu `make demo`).

### Langkah 3.1: lihat indeks CSR yang akan dinilai

Pilot 2 tidak mencari di web saat ada request; ia membaca **indeks** yang diisi
crawler. Demo berisi enam perusahaan karangan. Lihat dengan `csrctl`, CLI yang
sama dengan yang dipakai crawler sungguhan:

```bash
CSR_DB_PATH=data/demo/csr.db go run ./cmd/csrctl companies
```

Harapan:

```text
ID  NAME                   SOURCE  DOMAIN  DOMAIN STATUS  ACCESS  ROUTES  CONFIDENCE  STATUS    REVIEW
1   PT Terang Energi Tbk   seed    -       unverified     -       1       0.85        verified  -
2   PT Nusa Perbankan Tbk  seed    -       unverified     -       1       0.70        new       -
3   PT Sehat Farma         seed    -       unverified     -       1       0.80        new       -
4   PT Tirta Jaya Utama    seed    -       unverified     -       1       0.75        new       -
5   PT Lama Sekali Tbk     seed    -       unverified     -       0       0.60        new       -
6   PT Kebun Sumatera      seed    -       unverified     -       1       0.80        new       -
```

Perhatikan `STATUS`: hanya satu yang `verified` (keputusan manusia); sisanya
`new`. Perhatikan juga `ROUTES`: PT Lama Sekali punya **0** rute, yaitu tidak
ada halaman CSR yang masih bisa dicek. Ingat itu untuk langkah 3.3.

```bash
CSR_DB_PATH=data/demo/csr.db go run ./cmd/csrctl show "Terang Energi"
```

Harapan (dipersingkat): rute, profil (bidang, wilayah, jenis program, kanal
proposal), program, dan **bukti**: kutipan persis dari halaman perusahaan,
bernomor `[1]`, `[2]`, yang dirujuk oleh klaim di profil.

```text
evidence:
  [1] https://terangenergi.example/csr/beasiswa (fetched 2026-10-19)
      "Beasiswa Terang 2026 menyediakan 300 beasiswa untuk siswa SMA di Jawa Barat dan Banten. ..."
fit with institution: 90/100
```

Jalankan juga `show "Lama Sekali"` dan lihat baris `STALE: semua halaman CSR
yang tercatat kini hilang atau memblokir akses`.

### Langkah 3.2: kosakata dan katalog program

```bash
curl -s localhost:8080/api/v1/outreach/vocabulary -H "X-API-Key: $K" | python3 -c "
import sys,json; d=json.load(sys.stdin); print([g['name'] for g in d['focus_areas']])"
curl -s localhost:8080/api/v1/outreach/programs -H "X-API-Key: $K" | python3 -m json.tool | head -20
```

Harapan: sembilan kelompok bidang (`pendidikan`, `kesehatan`,
`pemberdayaan ekonomi`, `lingkungan`, `kebencanaan`, `air bersih`,
`infrastruktur`, `pelatihan`, `bantuan sosial`), dan dua program HI dari
`config/hi-programs.example.yaml`: `beasiswa-anak-desa` dan `air-bersih-dusun`.

Kosakata itu penting: kriteria program dicocokkan lewat kelompok ini
("beasiswa" dan "sekolah" dianggap `pendidikan`). Kamu akan melihat
akibatnya di langkah 3.5.

### Langkah 3.3: shortlist tanpa email (gratis)

```bash
curl -s -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $K" \
  -d '{"program_id":"beasiswa-anak-desa","emails":false}' \
  | python3 -c "
import sys,json
d=json.load(sys.stdin)
for t in d['term_coverage']: print('cakupan', t['kind'], t['term'], 'dikenali=', t['recognized'], 'perusahaan=', t['companies'])
for i,e in enumerate(d['entries'],1):
    print(i, e['company']['name'], 'skor', e['score'], 'verified', e['company']['verified'], 'basi', e['stale'], 'data', e['confirmed_at'])
    for r in e['reasons']: print('     alasan :', r)
    for n in e['notes']:   print('     catatan:', n)"
```

Harapan:

```text
cakupan focus pendidikan dikenali= True perusahaan= 3
cakupan region Jawa Barat dikenali= True perusahaan= 4
cakupan type beasiswa dikenali= True perusahaan= 2
1 PT Terang Energi Tbk skor 90 verified True basi False data 2026-10-22
     alasan : bidang fokus cocok: pendidikan (perusahaan: beasiswa, pendidikan)
     alasan : wilayah program cocok: Jawa Barat
     alasan : jenis program cocok: beasiswa
     alasan : halaman menyebut perusahaan membuka kemitraan atau menerima proposal
     alasan : tersedia kanal resmi pengajuan proposal: csr@terangenergi.example
     alasan : program aktif yang relevan: Beasiswa Terang 2026
2 PT Nusa Perbankan Tbk skor 75 verified False basi False data 2026-10-22
     ...
     catatan: Perusahaan belum diverifikasi orang (status baru). Pastikan datanya benar sebelum menghubungi.
3 PT Lama Sekali Tbk skor 45 verified False basi True data 2026-04-05
     ...
     alasan : semua program yang tercatat sudah berakhir atau tidak terlihat lagi di halaman sumber
     catatan: Data sudah lama: semua halaman CSR yang tercatat kini hilang atau memblokir akses. ...
     catatan: Kanal pengajuan proposal tidak diketahui; cari tahu cara menghubungi perusahaan ini.
     catatan: Belum ada program CSR aktif yang tercatat untuk perusahaan ini.
```

(Tanggal "data" mengikuti hari kamu menjalankan demo.)

Beberapa hal untuk diperiksa:

1. **Hanya tiga dari enam** perusahaan masuk. Sehat Farma (kesehatan), Tirta Jaya
   (air bersih), dan Kebun Sumatera (lingkungan) tidak bekerja di bidang
   `pendidikan`; syarat masuk adalah `FocusMatch` (`csrprospects.Source.Prospects`).
2. **Skor bisa dijumlahkan sendiri.** Terang: bidang 25 + wilayah 20 + jenis
   10 + membuka kemitraan 15 + kanal proposal 10 + program aktif relevan 10
   = **90**. Nusa Perbankan: 25 + nasional 15 + 15 + 10 + 10 = **75**. Lama Sekali:
   25 + 20 + 10 − 10 (semua programnya sudah berakhir) = **45**. Tabel
   bobotnya ada di [panduan pilot 2](pilot-2-shortlist-pendana.md#dari-mana-skor-datang);
   kodenya `InstitutionProfile.Score` dan `scorePrograms` di
   `internal/websearch/csr/score.go`.
3. **Perusahaan basi tetap tampil, diurutkan paling akhir, dan diberi label.**
   Tanggal datanya 2026-04-05: sudah lama. Diurutkan oleh `Index.FindProspects`.
4. Alasan dan catatan datang dari **data indeks**, bukan dari model. Tidak ada
   panggilan model sama sekali di langkah ini (`emails: false`); lihat
   `input_tokens` = 0 di `trace`.

### Langkah 3.4: ubah kriteria, ubah hasilnya

```bash
for R in '"Jawa Barat"' '"Papua"'; do
  curl -s -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $K" \
    -d "{\"program\":{\"name\":\"Latihan\",\"focus_areas\":[\"pendidikan\"],\"regions\":[$R],\"program_types\":[\"beasiswa\"]},\"emails\":false}" \
    | python3 -c "import sys,json; print([(e['company']['name'],e['score']) for e in json.load(sys.stdin)['entries']])"
done
```

Harapan:

```text
[('PT Terang Energi Tbk', 90), ('PT Nusa Perbankan Tbk', 75), ('PT Lama Sekali Tbk', 45)]
[('PT Terang Energi Tbk', 75), ('PT Nusa Perbankan Tbk', 75), ('PT Lama Sekali Tbk', 25)]
```

Dengan wilayah **Papua**, skor Terang turun dari 90 ke 75: poin wilayah 20
(halaman menyebut Jawa Barat) berganti 5 (hanya label wilayah dari data awal,
"Nasional", yang belum terbukti di halaman). Nusa Perbankan, yang berskala
nasional, tidak terpengaruh. Skor keduanya sama, sehingga **urutan ditentukan
oleh keyakinan perusahaan** (0,85 lawan 0,70, kolom `CONFIDENCE` di
`csrctl companies`), kriteria terakhir di `Index.FindProspects`. Skor tidak
disimpan di mana pun: ia dihitung dari program yang kamu kirim, karena
`csrprospects.Source.Prospects` membuat `csr.Index` baru di setiap request.

### Langkah 3.5: kriteria yang salah

```bash
curl -s -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $K" \
  -d '{"program":{"name":"Latihan","focus_areas":["pendidikan","olahraga"],"regions":["Papua"]},"emails":false}' \
  | python3 -c "
import sys,json; d=json.load(sys.stdin)
for w in d['warnings']: print('PERINGATAN:', w)
for t in d['term_coverage']: print('cakupan', t['kind'], t['term'], 'dikenali=', t['recognized'], 'perusahaan=', t['companies'])"
```

Harapan:

```text
PERINGATAN: Bidang "olahraga" bukan salah satu yang dikenali sistem (pendidikan, kesehatan, ...). Ia hanya cocok dengan perusahaan yang memakai kata yang sama persis: belum ada perusahaan di indeks yang memakainya. Periksa ejaannya, atau pakai istilah yang dikenali.
cakupan focus pendidikan dikenali= True perusahaan= 3
cakupan focus olahraga dikenali= False perusahaan= 0
cakupan region Papua dikenali= True perusahaan= 1
```

Dua hal yang menarik. **Papua tidak diperingatkan**: ada satu perusahaan
berskala nasional (Nusa Perbankan) yang dihitung mencakup wilayah mana pun,
jadi cakupannya 1, bukan 0. Sedangkan **"olahraga" diperingatkan** karena
bukan kelompok yang dikenali. Bila **hanya** "olahraga" yang kamu minta:

```bash
curl -s -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $K" -w "\nHTTP %{http_code}\n" \
  -d '{"program":{"name":"Latihan","focus_areas":["olahraga"]},"emails":false}'
```

Harapan: **HTTP 422** `no_prospects`, dan pesan errornya memuat peringatan yang
sama. Hasil kosong yang tidak menjelaskan diri adalah hasil yang membuang
waktu; di sini alasannya langsung terlihat. Kodenya: `outreach.Warnings`
(`domain/outreach/criteria.go`) dan `prospecting.NoProspectsError`.

Bandingkan juga dengan error lain:

```bash
curl -s -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $K" -d '{"program_id":"tidak-ada"}' -w "\nHTTP %{http_code}\n"
curl -s -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $K" -d '{"program_id":"beasiswa-anak-desa","program":{"name":"x","focus_areas":["y"]}}' -w "\nHTTP %{http_code}\n"
```

Harapan: 404 `program_not_found`, dan 400 "give either a program_id or the
program itself, not both" (`Service.resolveProgram`).

### Langkah 3.6: shortlist dengan email

Sekarang minta email juga (default bila `emails` tidak diisi). Dua perusahaan
saja:

```bash
curl -s -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $K" \
  -d '{"program_id":"beasiswa-anak-desa","limit":2}' > hasil.json
python3 - <<'EOF'
import json
d = json.load(open("hasil.json"))
e = d["entries"][0]
print(e["company"]["name"]); print("Subjek:", e["email"]["subject"]); print(e["email"]["body"])
for b in e["email"]["basis"]: print("DASAR:", b["title"], b["date"], b["url"])
print("trace:", d["trace"]["emails"])
print("token masuk/keluar:", d["trace"]["input_tokens"], d["trace"]["output_tokens"], "biaya (Rp):", d["trace"]["estimated_cost_rupiah"])
EOF
```

Harapan:

```text
PT Terang Energi Tbk
Subjek: Kemitraan program Beasiswa Anak Desa
Yth. Tim CSR PT Terang Energi Tbk,

[DEMO] Kami dari Human Initiative membaca di "Program TJSL Terang Energi" (diterbitkan 2026-09-22) bahwa Beasiswa Terang 2026 menyediakan 300 beasiswa untuk siswa SMA di Jawa Barat dan Banten. Kami mengelola program "Beasiswa Anak Desa" dan ingin membuka percakapan tentang kemungkinan kemitraan.

[Nama pengirim]
[Jabatan]
[Kontak]
DASAR: Program TJSL Terang Energi 2026-09-22 https://terangenergi.example/csr/beasiswa
trace: [{'company': 'PT Terang Energi Tbk', 'attempts': 1, 'input_tokens': 528, 'output_tokens': 114, 'outcome': 'ok'}, ...]
```

(Tanggal terbit = sebulan sebelum hari kamu menjalankan demo.)

Yang perlu kamu perhatikan:

1. Email **menyebut tanggal sumbernya** ("diterbitkan 2026-09-22"): aturan di
   prompt, supaya informasi lama tidak disajikan sebagai kabar terbaru.
2. Email **mengutip bukti bernomor**; teks dan URL bukti ditampilkan di
   `basis`, sehingga pembaca bisa mencocokkan klaim ke sumbernya.
3. Nama dan kontak pengirim dibiarkan sebagai **placeholder**: kode tidak tahu
   siapa pengirimnya, dan model dilarang mengarangnya.
4. `trace` mencatat percobaan, token, dan hasil per email. Biaya muncul karena
   demo mengisi harga contoh (`MODEL_*_RUPIAH_PER_MILLION_TOKENS` di produksi).

Fungsi yang bekerja: `Service.draftEmails` (paralel, maksimal 4) →
`Service.draftEmail` → `trimEvidence` → `buildPrompt` → model → `parseEmail` →
`outreach.VerifyEmail`. Buka `draftEmail` di
`internal/application/prospecting/email.go`.

### Langkah 3.7: unduh sebagai Word

```bash
curl -s -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $K" \
  -d '{"program_id":"beasiswa-anak-desa","format":"docx"}' -o calon-mitra.docx -D - | grep -i content-disposition
open calon-mitra.docx
```

Harapan: `filename="calon-mitra-csr-<tanggal>.docx"`. Di dalamnya: ringkasan,
lalu satu bagian per perusahaan (catatan **"Periksa sebelum bertindak"**,
alasan, program, email, dan **dasar informasi**), dan "Jejak proses" di akhir.
Renderer yang sama dengan pilot 1 (`reportdocx`), lewat `Shortlist.Draft`.

### Langkah 3.8: shortlist dari command line

`csrctl shortlist` menilai indeks **tanpa model dan tanpa harness**.
Berguna untuk menyetel skor dan untuk lembar penilaian relevansi:

```bash
CSR_DB_PATH=data/demo/csr.db go run ./cmd/csrctl shortlist -program config/program.example.yaml
CSR_DB_PATH=data/demo/csr.db go run ./cmd/csrctl shortlist -program config/program.example.yaml -csv > lembar.csv
```

Harapan: tabel tiga perusahaan, dan CSV dengan dua kolom kosong di ujung,
`relevant (y/n)` dan `comment`, untuk diisi Penanggung Jawab.

Perhatikan: skor Nusa Perbankan di sini **85**, bukan 75 seperti di langkah 3.3.
Penyebabnya: `config/program.example.yaml` meminta jenis dukungan
`[beasiswa, pelatihan]` dan Nusa menjalankan program pelatihan (+10),
sedangkan program katalog hanya `beasiswa`. **Skor bergantung pada program
yang diminta.**

### Langkah 3.9: lihat penolakan email bekerja

Dua mode ulah untuk email. **Terminal 1:** `Ctrl+C`, lalu:

```bash
make demo QUIRK=fake-email
```

Ulangi langkah 3.6 (hanya `limit: 1` cukup):

```bash
curl -s -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $K" \
  -d '{"program_id":"beasiswa-anak-desa","limit":1}' \
  | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['trace']['emails']); print('rahasia' in d['entries'][0]['email']['body'])"
```

Harapan: `attempts: 2`, dan `False` (alamat karangan tidak ada di email akhir).
Di log terminal 1:

```text
level=WARN msg="email rejected" company="PT Terang Energi Tbk" attempt=1
  reason="\"direktur@rahasia.example\" appears in no evidence or program text; never invent contact details"
```

Percobaan pertama model palsu menyisipkan alamat email karangan.
`outreach.VerifyEmail` menolaknya karena alamat itu tidak ada di bukti mana
pun, `draftEmail` mengirim alasannya kembali ke model, dan percobaan kedua
bersih. Alamat karangan bisa mengirim proposal ke orang asing, karena itu ini
**penolakan**, bukan sekadar catatan.

Sekarang yang sebaliknya, hal yang hanya **dicatat**:

```bash
# terminal 1: Ctrl+C, lalu
make demo QUIRK=invent-number
```

Ulangi 3.6. Harapan: email berhasil dalam satu percobaan, tetapi `notes`
perusahaan itu berisi:

```text
Angka di email ini tidak ada di bukti yang dikutip maupun di data program: 450. Periksa sebelum dikirim.
```

Email itu layak dipakai tetapi ada sesuatu yang harus dilihat manusia.
Perbedaan **`problems`** (tolak dan ulangi) dan **`notes`** (tandai) ada di
`VerifyEmail`; bacalah test `TestVerifyEmail_Problems` dan
`TestVerifyEmail_AcceptsKnownLinksAndFlagsInventedNumbers`.

(`QUIRK=bad-json-once` juga berlaku: `attempts` menjadi 2 untuk setiap email.)

### Langkah 3.10: program lain

```bash
curl -s -X POST localhost:8080/api/v1/outreach -H "X-API-Key: $K" \
  -d '{"program_id":"air-bersih-dusun","emails":false}' \
  | python3 -c "import sys,json; print([(e['company']['name'],e['score']) for e in json.load(sys.stdin)['entries']])"
```

Harapan: `[('PT Tirta Jaya Utama', 90), ('PT Sehat Farma', 55)]`. Program yang
berbeda memunculkan perusahaan yang berbeda: perusahaan air bersih naik ke atas
dan perusahaan pendidikan menghilang.

### Langkah 3.11: baca kodenya sekarang

1. `internal/application/prospecting/service.go`: `Run`, `resolveProgram`, `draftEmails`.
2. `internal/adapters/outbound/csrprospects/source.go`: `Prospects`.
3. `internal/websearch/csr/score.go`: `Score`, `scorePrograms`, `Coverage`.
4. `internal/application/prospecting/email.go`: `draftEmail`.
5. `internal/domain/outreach/verify.go`: `VerifyEmail`.
6. `internal/application/prospecting/service_test.go`: baca nama test sebagai
   daftar perilaku.

Lanjut ke latihan di akhir [panduan pilot 2](pilot-2-shortlist-pendana.md#latihan-ubah-sesuatu).

---

## Bagian 4: menjalankan dengan Gemini asli

Demo membuktikan **alurnya**. Mutu tulisan dan mutu indeks baru bisa dinilai
dengan kunci asli dan data nyata. Perbedaannya:

| | Demo | Asli |
|---|---|---|
| Model | Palsu, mengutip dokumen | Gemini: menulis sendiri, bisa salah |
| Angka laporan | API donasi tiruan | API donasi asli (`REPORT_API_URL`), belum ada |
| Indeks CSR | 6 perusahaan karangan, langsung jadi | Hasil `csrctl crawl` dari situs sungguhan |
| Catatan reviewer | Muncul hanya bila memakai `QUIRK` | Muncul sendiri bila Gemini menyebut sesuatu yang tak berdasar |

Untuk menjalankan harness asli (bukan demo) dengan API donasi tiruan dan kunci
Gemini, ikuti [README panduan, Tingkat 2](README.md#tingkat-2-dengan-kunci-gemini).
Yang berubah dari demo hanya `make run` menggantikan `make demo`.

Hal pertama yang layak kamu amati dengan Gemini asli: apakah `notes` muncul
sendiri tanpa quirk, dan seberapa sering `attempts` bernilai 2. Itu data untuk
menyetel prompt (`narrative.go`, `email.go`) dan daftar tugas di
[TODO.md](../../TODO.md).

## Bagian 5: uji pemahamanmu

Kamu sudah paham bila bisa menjawab ini tanpa membuka dokumen. Jawaban ada di
dalam kurung.

1. Mengapa total laporan tidak dikirim oleh API donasi? (Dihitung dari baris
   tabel supaya tidak mungkin berbeda; `Figures.TotalReceived`.)
2. Di langkah mana laporan memanggil model, dan berapa kali paling banyak?
   (`writeNarratives`, sekali, dua kali bila jawaban ditolak.)
3. Apa yang terjadi bila model menyebut angka yang tidak ada di dokumen?
   (Laporan tetap dibuat, angkanya masuk `notes`; `checkNumbers`.)
4. Mengapa format yang salah ditolak sebelum memanggil API donasi?
   (Request yang pasti gagal tidak boleh memakan biaya atau waktu.)
5. Mengapa PT Sehat Farma tidak masuk shortlist program beasiswa?
   (Bidangnya kesehatan; `FocusMatch` harus benar.)
6. Mengapa PT Lama Sekali tetap tampil walau datanya basi, dan di mana posisinya?
   (Basi tetap ditampilkan dengan label dan diurutkan terakhir.)
7. Apa bedanya email yang *ditolak* dan email yang *dicatat*?
   (`problems` membuat model mengulang; `notes` ditandai untuk manusia.)
8. Mengapa alamat email karangan ditolak, bukan hanya dicatat?
   (Bisa mengirim proposal ke orang asing.)
9. Di mana kamu menambahkan format keluaran PDF? (Adapter baru untuk
   `outbound.DraftRenderer`, didaftarkan di `bootstrap`.)
10. Mengapa skor Nusa Perbankan berbeda antara 3.3 dan 3.8? (Program yang
    diminta berbeda; indeks dinilai ulang untuk tiap program.)

## Bila ada masalah

| Gejala | Penyebab dan cara memperbaiki |
|---|---|
| `the harness cannot listen on 127.0.0.1:8080` | Port terpakai. Jalankan `go run ./cmd/pilotdemo -addr 127.0.0.1:8081` dan ganti `8080` di perintah |
| `the donation API cannot listen on 127.0.0.1:8090` | Sama; pakai `-mock-addr 127.0.0.1:8091` |
| `open config/hi-programs.example.yaml: no such file` | Jalankan dari root repo, atau beri `-programs /path/ke/file.yaml` |
| `unknown -quirk "..."` | Pilihan: `invent-number`, `bad-json-once`, `fake-email` |
| `401 unauthorized` | Kunci salah; pakai `demo-key-only-for-learning-0000000000` |
| Laporan menampilkan semua `Rp 0` | Periode di luar Januari–Juni 2026; API tiruan hanya punya data itu |
| `csrctl` mengatakan tidak ada perusahaan yang cocok | `CSR_DB_PATH` belum menunjuk `data/demo/csr.db`, atau demo belum pernah dinyalakan |
| Perubahanmu di kode tidak terlihat | Demo harus dihentikan dan dinyalakan ulang (`Ctrl+C`, `make demo`) |
| Ingin mengulang dari awal | `Ctrl+C` lalu `make demo`: indeks dibuat ulang setiap start |

Bila masalahnya tidak ada di tabel, jalankan `go test ./cmd/pilotdemo/ -v`.
Tes itu menjalankan seluruh demo lewat HTTP sungguhan, jadi kegagalannya
biasanya menunjuk langsung ke langkah yang rusak.
