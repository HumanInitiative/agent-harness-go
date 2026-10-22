# Arsitektur scale-out

Jawaban singkat: setup di [deployment-ec2.md](deployment-ec2.md) **bisa scale
up (vertikal) tetapi belum bisa scale out (horizontal)**. Penyebabnya ada di
kode aplikasi, bukan di ketiadaan orkestrator. Menambah Kubernetes atau Swarm
sekarang tidak akan membantu, karena replika kedua akan memecah data dan rate
limit. Dokumen ini menjelaskan apa yang harus diubah dulu, lalu arsitektur
scale-out yang murah, dan bagaimana scale up/down dilakukan tanpa
Kubernetes.

- [Kondisi sekarang](#kondisi-sekarang)
- [Apakah benar-benar perlu scale out?](#apakah-benar-benar-perlu-scale-out)
- [Tahap 1: scale up](#tahap-1-scale-up)
- [Tahap 2: scale out](#tahap-2-scale-out)
- [Perubahan kode yang diperlukan](#perubahan-kode-yang-diperlukan)
- [Infrastruktur tanpa Kubernetes](#infrastruktur-tanpa-kubernetes)
- [Kapan scale up dan down](#kapan-scale-up-dan-down)
- [Perkiraan biaya](#perkiraan-biaya)
- [Urutan pengerjaan](#urutan-pengerjaan)

## Kondisi sekarang

Empat hal masih terikat pada satu proses atau satu disk:

| Komponen | Di mana | Akibat bila ada 2 replika |
| -------- | ------- | ------------------------- |
| Riwayat percakapan | memori proses ([`adapters/outbound/memory`](../internal/adapters/outbound/memory/conversation_store.go)) | Pesan lanjutan yang mendarat di replika lain kehilangan konteks |
| Penjaga "satu giliran per percakapan" | [`application/agent/keyset.go`](../internal/application/agent/keyset.go) | Dua request untuk percakapan yang sama bisa berjalan bersamaan di replika berbeda |
| Rate limit per API key | [`adapters/inbound/httpapi/ratelimit.go`](../internal/adapters/inbound/httpapi/ratelimit.go) | Batas efektif menjadi N kali lipat |
| Indeks CSR | file SQLite di satu volume ([`websearch/csr/store.go`](../internal/websearch/csr/store.go)) | Replika di mesin lain tidak melihat file itu; berbagi lewat NFS/EFS tidak aman untuk SQLite |

README sudah mencatat tiga yang pertama di bagian
[Scaling beyond one instance](../README.md#scaling-beyond-one-instance). Baris
keempat melengkapinya. Kunci crawl ([`csr/lock.go`](../internal/websearch/csr/lock.go))
juga tersimpan di SQLite, jadi ikut pindah bersama indeks.

Kabar baiknya, port untuk percakapan sudah ada
([`ports/outbound/conversation.go`](../internal/ports/outbound/conversation.go)),
jadi menggantinya cukup dengan adapter baru tanpa menyentuh logika bisnis.

## Apakah benar-benar perlu scale out?

Tanyakan dulu apa yang sebenarnya dikejar:

- **Throughput.** Pekerjaan harness sebagian besar menunggu respons Gemini
  (I/O), bukan menghitung. Satu proses Go biasanya sanggup menahan ratusan
  request yang menunggu sekaligus, jadi kemungkinan besar batas pertama yang
  tercapai adalah **kuota Gemini**, bukan CPU server. Ini dugaan, belum
  diukur: jalankan load test pada `t4g.small` sebelum mengambil keputusan
  mahal.
- **Ketersediaan (HA).** Satu EC2 berarti beberapa menit mati saat instance
  rusak atau saat OS di-reboot. Kalau itu bisa diterima, cukup auto-recovery
  dan snapshot; tidak perlu scale out.
- **Kapasitas yang naik turun.** Hanya relevan kalau beban memang bergelombang
  tajam. Untuk layanan internal Human Initiative, hampir pasti tidak.

Scale out layak dikerjakan hanya bila HA atau throughput terbukti jadi masalah.

## Tahap 1: scale up

Tanpa mengubah kode, dan paling murah:

1. Naikkan tipe instance (`t4g.small` → `t4g.medium` → `t4g.large`). Stop
   instance, ubah tipe, start lagi: downtime beberapa menit, alamat tetap
   karena memakai Elastic IP.
2. Naikkan `RATE_LIMIT_PER_MINUTE` dan `RATE_LIMIT_BURST` di `.env` sesuai
   kebutuhan klien.
3. Pasang CloudWatch alarm `StatusCheckFailed` dengan aksi *recover* agar
   instance pulih sendiri.
4. Naikkan kuota Gemini (tier berbayar) bila 429 mulai muncul. Ini hampir pasti
   hambatan pertama.

Tahap ini cukup untuk waktu yang lama.

## Tahap 2: scale out

```
                       Internet
                          │
                 ┌────────▼────────┐
                 │ ALB (HTTPS, ACM)│   health check: /readyz
                 └───┬─────────┬───┘
                     │         │
              ┌──────▼───┐ ┌───▼──────┐      Auto Scaling Group
              │ harness  │ │ harness  │ ...  (min 2, max N)
              │ +searxng │ │ +searxng │      tanpa state lokal
              └──┬────┬──┘ └──┬────┬──┘
                 │    │       │    │
        ┌────────▼┐  ┌▼───────▼┐   │
        │  Redis  │  │Postgres │◀──┘ baca indeks CSR
        │percakapan│ │ indeks  │
        │ kunci,   │ │ CSR     │◀──── csr-crawler (1 instance saja,
        │ rate limit│ └─────────┘      EC2 kecil / task terjadwal)
        └──────────┘
```

Prinsipnya: **semua state keluar dari instance harness**, sehingga instance
boleh ditambah, dihapus, atau mati kapan saja.

| Komponen | Layanan | Catatan |
| -------- | ------- | ------- |
| Load balancer | ALB + sertifikat ACM (gratis) | Menggantikan Caddy. Health check ke `/readyz` |
| Harness | Auto Scaling Group, minimal 2 di dua AZ | Stateless setelah perubahan kode di bawah |
| SearXNG | Satu per instance harness (sidecar di compose) | Tanpa state, jadi aman diduplikasi. Tidak perlu layanan bersama |
| Percakapan, kunci giliran, rate limit | ElastiCache Redis (atau Valkey) | Satu node kecil cukup. Percakapan memang tidak durable, jadi kehilangan node bukan bencana |
| Indeks CSR | RDS PostgreSQL | Lihat pilihan di bawah |
| Crawler harian | Tepat satu proses: EC2 kecil sendiri, atau task terjadwal | Jangan ikut Auto Scaling Group, supaya crawl tidak berjalan ganda |
| Rahasia | SSM Parameter Store / Secrets Manager | Menggantikan `.env` di disk |
| Image | ECR | Server tidak lagi membangun image sendiri |

### Pilihan untuk indeks CSR

Indeks adalah beban baca-berat yang ditulis satu crawler sehari sekali, jadi ada
dua jalan:

- **A. Pindah ke PostgreSQL.** Paling bersih dan umum, tetapi paling banyak
  pekerjaan: [`csr/store.go`](../internal/websearch/csr/store.go) memakai SQLite
  dengan FTS5 (pencarian teks penuh) dan skema `schema_v1..v4.sql`. FTS5 perlu
  diganti dengan `tsvector`, dan kunci crawl dipindah ke `pg_advisory_lock` atau
  tabel serupa.
- **B. Tetap SQLite, replikasi baca.** Crawler menulis SQLite, lalu
  [Litestream](https://litestream.io/) mereplikasinya ke S3. Setiap instance
  harness memulihkan salinan baca-saja saat boot atau berkala. Perubahan kode
  jauh lebih kecil dan tidak ada biaya RDS, tetapi data bisa tertinggal sebentar
  setelah crawl. Cocok karena indeks memang hanya berubah sekali sehari.

Rekomendasi: mulai dari **B** bila alasan scale out adalah HA, dan pilih **A**
hanya bila ada kebutuhan lain pada database relasional.

## Perubahan kode yang diperlukan

Status pengerjaan (urutan dari yang terpenting):

1. **Selesai: `ConversationStore` berbasis Redis**
   ([`adapters/outbound/redisstore`](../internal/adapters/outbound/redisstore/conversation_store.go)).
   Daftar Redis yang dipangkas ke `HISTORY_WINDOW_MESSAGES + 2` pesan dan
   TTL `CONVERSATION_TTL_MINUTES` yang diperbarui tiap append. Jumlah
   percakapan tidak dibatasi aplikasi; batasi di server Redis dengan
   `maxmemory` dan `maxmemory-policy allkeys-lru`.
2. **Selesai: kunci giliran terdistribusi.** `keySet` diganti port
   `outbound.TurnGuard` dengan implementasi memori dan Redis
   (`SET NX PX`, dilepas dengan skrip yang memeriksa pemilik). TTL klaim =
   `REQUEST_TIMEOUT_SECONDS` + 15 detik, jadi instance yang mati tidak
   mengunci percakapan lebih lama dari itu.
3. **Selesai: konfigurasi.** `REDIS_URL` (kosong = mode memori, perilaku lama)
   dan `REDIS_KEY_PREFIX`, dipilih di
   [`cmd/harness/main.go`](../cmd/harness/main.go). Percakapan dan kunci giliran
   selalu berpindah bersama. Tes dua instance yang berbagi satu Redis ada di
   [`multi_instance_test.go`](../internal/application/agent/multi_instance_test.go).
4. **Belum: rate limit bersama.** Tiga jalan, dari termudah:
   - terima batas N kali lipat dan turunkan nilai per instance (paling murah,
     kurang presisi),
   - hitung di Redis dengan token bucket atau sliding window,
   - pindahkan ke AWS WAF di depan ALB (rate-based rule), dengan tarif terpisah.
5. **Belum: indeks CSR** sesuai pilihan A atau B di atas.
6. **Belum: pemeriksaan Redis di `/readyz`.** Sengaja ditunda: saat Redis mati
   semua instance gagal bersamaan, jadi mengeluarkannya dari load balancer
   tidak menolong. Aplikasi sudah gagal cepat (timeout 2 detik) dan gagal saat
   start bila Redis tidak terjangkau.

Karena arsitekturnya heksagonal, perubahan 1 sampai 3 tidak menyentuh logika
agen selain penggantian `keySet` dengan port.

## Infrastruktur tanpa Kubernetes

Scale up dan down **tidak membutuhkan orkestrator** selama aplikasinya
stateless. AWS punya dua jalan yang jauh lebih sederhana daripada Kubernetes:

### Opsi 1: Auto Scaling Group + ALB (paling dekat dengan setup sekarang)

- Buat **launch template** dengan AMI Ubuntu, instance profile (ECR + SSM), dan
  *user data* yang memasang Docker, menarik image dari ECR, lalu menjalankan
  compose (harness + SearXNG) memakai kredensial dari Parameter Store.
- **Auto Scaling Group** dengan `min=2`, `max=N`, tersebar di dua AZ. Target
  group ALB memakai health check `/readyz`.
- **Scaling policy**: target tracking pada `ALBRequestCountPerTarget`, atau CPU
  rata-rata sekitar 60%. Karena harness lebih banyak menunggu I/O, jumlah
  request per instance biasanya sinyal yang lebih baik daripada CPU.
- **Deploy**: bangun image di CI, dorong ke ECR, lalu *Instance Refresh* pada
  ASG untuk mengganti instance bergiliran tanpa downtime (pastikan
  *minimum healthy percentage* 50 atau lebih).
- Dengan `terminationGracePeriod` yang setara: atur *connection draining*
  (deregistration delay) di target group minimal 80 detik, mengikuti
  `stop_grace_period` di compose.

Keunggulan: sangat mirip dengan yang sudah Anda jalankan, tidak ada konsep baru
selain ASG dan ALB.

### Opsi 2: ECS di Fargate (tanpa mengurus server)

- Definisikan *task* untuk harness (dan SearXNG sebagai kontainer kedua di task
  yang sama), lalu *service* dengan autoscaling berbasis request atau CPU.
- Tidak ada biaya control plane dan tidak ada OS yang perlu di-patch. Rolling
  deploy dan health check sudah bawaan.
- Crawler menjadi *scheduled task* (EventBridge) pukul 02:00 WIB, yang otomatis
  menjamin hanya satu proses.
- Biaya per vCPU lebih tinggi daripada EC2 untuk beban konstan, tetapi lebih
  hemat tenaga.

Rekomendasi: **Opsi 2 (ECS Fargate)** bila tim tidak ingin merawat server,
**Opsi 1** bila ingin tetap dekat dengan EC2 dan biaya serendah mungkin.
Keduanya bukan Kubernetes, dan keduanya bisa diganti belakangan karena
imagenya sama. Peta jalan Kubernetes ada di
[kubernetes.md](kubernetes.md).

## Kapan scale up dan down

Pantau, lalu putuskan dengan angka, bukan firasat:

| Sinyal | Artinya | Tindakan |
| ------ | ------- | -------- |
| Banyak respons 429/503 dari model | Kuota Gemini habis | Naikkan kuota; menambah replika **tidak** membantu |
| CPU instance > 70% terus-menerus | Beban komputasi (parsing PDF, build) | Scale up, lalu scale out |
| Latensi p95 naik bersama jumlah request bersamaan | Instance jenuh | Scale out |
| Memori > 80% | SearXNG atau PDF besar | Naikkan tipe instance |
| `StatusCheckFailed` | Host bermasalah | Auto-recover; bila sering, scale out untuk HA |

Aturan scale-in: jangan menurunkan di bawah 2 instance setelah memilih HA, dan
beri jeda pendinginan (cooldown) yang cukup, karena giliran model bisa
berlangsung sampai `REQUEST_TIMEOUT_SECONDS`.

## Perkiraan biaya

Kasar, region Singapura, per bulan; verifikasi di AWS Pricing Calculator.

| Setup | Isi | Kira-kira |
| ----- | --- | --------- |
| Sekarang | 1× `t4g.small`, EBS, Elastic IP, Caddy | ~US$18 |
| Scale up | 1× `t4g.medium` atau `large` | ~US$30 sampai 60 |
| Scale out minimal (opsi 1) | ALB (~16) + 2× `t4g.small` (~24) + Redis `t4g.micro` (~12) + crawler `t4g.micro` (~6) + IPv4/EBS/NAT bila ada | ~US$75 sampai 110 |
| Ditambah RDS (indeks pilihan A) | RDS `db.t4g.micro` + storage | +US$15 sampai 25 |

Scale out menaikkan biaya sekitar 4 sampai 6 kali lipat. Itu harga ketersediaan
tinggi, bukan harga throughput.

## Urutan pengerjaan

1. Deploy satu-EC2 di [deployment-ec2.md](deployment-ec2.md), lalu ukur: load
   test dan pantau kuota Gemini.
2. Scale up dan siapkan auto-recovery. Berhenti di sini jika kebutuhan HA belum
   nyata.
3. Bila HA atau throughput terbukti perlu, kerjakan perubahan kode (daftar di
   atas) dengan Redis dan pilihan indeks CSR, di bawah flag konfigurasi agar
   mode satu-instance tetap berfungsi.
4. Pindahkan build ke CI + ECR.
5. Naik ke ASG + ALB atau ECS Fargate. Kubernetes baru dipertimbangkan jika ada
   banyak layanan lain yang berbagi platform.
