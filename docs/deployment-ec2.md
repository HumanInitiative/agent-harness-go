# Deployment di AWS EC2 dengan Docker

Panduan ini memindahkan deployment manual di EC2 ke Docker Compose pada **satu
instance EC2**. File pendukungnya ada di [`deploy/ec2/`](../deploy/ec2/).

- [Perlu Docker Swarm atau Kubernetes?](#perlu-docker-swarm-atau-kubernetes)
- [Arsitektur dan perkiraan biaya](#arsitektur-dan-perkiraan-biaya)
- [Langkah 1: siapkan AWS](#langkah-1-siapkan-aws)
- [Langkah 2: pasang Docker di server](#langkah-2-pasang-docker-di-server)
- [Langkah 3: ambil kode dan isi konfigurasi](#langkah-3-ambil-kode-dan-isi-konfigurasi)
- [Langkah 4: jalankan](#langkah-4-jalankan)
- [Deploy versi baru dan rollback](#deploy-versi-baru-dan-rollback)
- [Backup](#backup)
- [Operasional sehari-hari](#operasional-sehari-hari)
- [Kapan naik ke yang lebih besar](#kapan-naik-ke-yang-lebih-besar)

## Perlu Docker Swarm atau Kubernetes?

**Tidak. Pakai Docker Compose di satu EC2.**

Swarm dan Kubernetes berguna untuk menjalankan banyak replika di banyak mesin.
Aplikasi ini justru dirancang untuk **satu instance saja** (lihat bagian Scope di
[README](../README.md)):

- percakapan disimpan di memori proses,
- indeks CSR berupa file SQLite di satu volume,
- batas rate limit dihitung per instance,
- hanya satu crawl boleh berjalan dalam satu waktu.

Jadi replika kedua tidak akan menambah ketersediaan, malah memecah data dan
rate limit. Biayanya juga tidak sepadan: control plane EKS saja sekitar US$73
per bulan sebelum ada satu pun server, dan Kubernetes menambah beban belajar dan
perawatan untuk tim kecil. Docker Swarm lebih ringan, tetapi pengembangannya
nyaris berhenti, jadi tidak layak jadi investasi baru.

Docker Compose sudah cukup: satu file mendefinisikan harness, crawler harian,
dan SearXNG, dengan `restart: unless-stopped` agar semuanya hidup lagi setelah
reboot.

## Arsitektur dan perkiraan biaya

```
Internet ──443──▶ Caddy (HTTPS otomatis)
                    │
                    ▼
                  harness :8080 ──▶ searxng (privat, tanpa port publik)
                    │
        csr-crawler (02:00 WIB) ──▶ volume csr-data (SQLite)
```

Semua berjalan di satu EC2 di **region Singapura (`ap-southeast-1`)**. Itu
disengaja: filter ISP Indonesia memblokir sebagian mesin pencari, dan hasil
pencarian akan diam-diam memburuk kalau server ada di jaringan itu.

| Komponen | Pilihan termurah yang masih layak | Perkiraan per bulan |
| -------- | --------------------------------- | ------------------- |
| EC2 | `t4g.small` (2 vCPU ARM, 2 GB) | ~US$12 |
| Disk | EBS gp3 20 GB | ~US$2 |
| IPv4 publik (Elastic IP) | 1 alamat | ~US$4 |
| HTTPS | Caddy di server yang sama (gratis) | US$0 |
| Akses admin | SSM Session Manager (gratis, tanpa port SSH) | US$0 |
| **Total** | | **~US$18** |

Angka di atas perkiraan kasar dan bisa berubah; cek
[AWS Pricing Calculator](https://calculator.aws/) sebelum memutuskan.

Mengapa pilihan ini:

- **`t4g.small`, bukan `micro`.** SearXNG memakai sekitar 300 sampai 500 MB, dan
  build image Go butuh memori lebih. 1 GB terlalu mepet. Semua image di sini
  (golang, debian, caddy, searxng) tersedia untuk ARM64, dan CGO dimatikan, jadi
  instance Graviton aman dan lebih murah daripada `t3.small`.
- **Caddy, bukan Application Load Balancer.** ALB mulai sekitar US$16 per bulan,
  lebih mahal daripada servernya sendiri, dan tidak ada manfaatnya untuk satu
  instance.
- **SSM Session Manager, bukan SSH.** Tidak ada port 22 yang terbuka, tidak ada
  key yang harus dijaga atau dirotasi.
- **Tanpa registry.** Image dibangun langsung di server. Cukup untuk satu server;
  pindah ke ECR atau GHCR kalau nanti butuh lebih dari satu mesin.

Penghematan lanjutan: Compute Savings Plan atau Reserved Instance 1 tahun
menurunkan biaya EC2 cukup besar kalau server memang akan menyala terus.

## Langkah 1: siapkan AWS

1. **Domain.** Siapkan nama, misalnya `harness.humaninitiative.org`. Caddy butuh
   nama domain untuk mendapat sertifikat Let's Encrypt.
2. **IAM role untuk EC2.** Buat role dengan policy terkelola
   `AmazonSSMManagedInstanceCore` dan pasang ke instance. Ini memungkinkan masuk
   lewat Session Manager.
3. **Security group.** Inbound hanya:

   | Port | Sumber | Fungsi |
   | ---- | ------ | ------ |
   | 80 | `0.0.0.0/0` | Tantangan sertifikat dan redirect ke HTTPS |
   | 443 | `0.0.0.0/0` | API |

   Jangan buka 22 dan 8080. Outbound dibiarkan terbuka (Gemini, pencarian web,
   dan unduhan laporan CSR butuh itu).
4. **Luncurkan instance.**
   - AMI: Ubuntu Server 24.04 LTS (arm64). Agent SSM sudah terpasang.
   - Tipe: `t4g.small`. Disk: gp3 20 GB.
   - Aktifkan **IMDSv2 wajib** dan **terminasi protection**.
5. **Elastic IP.** Alokasikan, asosiasikan ke instance, lalu buat DNS record
   `A` dari domain ke alamat itu. Tanpa Elastic IP, alamat berubah setiap
   instance berhenti dan dinyalakan.

Masuk ke server dengan AWS CLI (plugin Session Manager terpasang) atau dari
konsol AWS (**Connect → Session Manager**):

```bash
aws ssm start-session --target i-xxxxxxxxxxxxxxxxx --region ap-southeast-1
```

## Langkah 2: pasang Docker di server

```bash
# repositori resmi Docker
sudo apt-get update && sudo apt-get install -y ca-certificates curl git
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin

# Docker menyala saat boot, user ubuntu boleh memakainya
sudo systemctl enable --now docker
sudo usermod -aG docker "$USER"   # keluar dan masuk lagi agar berlaku

docker compose version            # harus v2.24 atau lebih baru
```

Batasi ukuran log kontainer agar disk tidak penuh. Buat
`/etc/docker/daemon.json`, lalu `sudo systemctl restart docker`:

```json
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" }
}
```

Tambahkan swap 2 GB agar build tidak mati kehabisan memori:

```bash
sudo fallocate -l 2G /swapfile && sudo chmod 600 /swapfile
sudo mkswap /swapfile && sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

## Langkah 3: ambil kode dan isi konfigurasi

```bash
git clone https://github.com/HumanInitiative/agent-harness-go.git
cd agent-harness-go
cp .env.example .env
chmod 600 .env
```

Isi `.env` (`nano .env`). Yang wajib untuk deployment ini:

| Variabel | Isi |
| -------- | --- |
| `GEMINI_API_KEY` | Kunci dari Google AI Studio |
| `API_KEYS` | Kunci klien, minimal 32 karakter: `openssl rand -hex 32` |
| `SEARXNG_SECRET` | `openssl rand -hex 32` |
| `HARNESS_DOMAIN` | Domain dari langkah 1, misalnya `harness.humaninitiative.org` |

`HARNESS_DOMAIN` hanya dipakai oleh Caddy; tambahkan sebagai baris baru di
`.env`. Biarkan `SWAGGER_ENABLED=false` kecuali dokumentasi API memang untuk
publik.

Repositori yang privat butuh cara mengkloning: pakai deploy key read-only atau
fine-grained token yang hanya punya akses baca ke repo ini.

## Langkah 4: jalankan

```bash
deploy/ec2/deploy.sh
```

Skrip ini mengambil `origin/main`, membangun image, menjalankan
`docker compose up -d`, lalu menunggu `/readyz` menjawab. Ia memakai dua file
compose sekaligus:

- [`deploy/docker-compose.yml`](../deploy/docker-compose.yml): harness, crawler,
  SearXNG.
- [`deploy/ec2/docker-compose.caddy.yml`](../deploy/ec2/docker-compose.caddy.yml):
  menambah Caddy dan menutup port 8080 dari luar.

Uji dari laptop:

```bash
curl https://harness.humaninitiative.org/healthz
```

Sertifikat pertama biasanya terbit dalam setengah menit. Kalau gagal, hampir
selalu karena DNS belum mengarah ke Elastic IP atau port 80 belum terbuka di
security group (`docker compose ... logs caddy`).

## Deploy versi baru dan rollback

Alur normal: gabungkan perubahan ke `main` lewat PR (CI harus hijau), masuk ke
server, lalu:

```bash
cd ~/agent-harness-go
deploy/ec2/deploy.sh
```

Rollback ke commit sebelumnya:

```bash
DEPLOY_REF=<sha-sebelumnya> deploy/ec2/deploy.sh
```

Container lama diberi waktu `stop_grace_period` (80 detik) untuk menyelesaikan
request yang sedang berjalan, jadi downtime hanya beberapa detik saat container
diganti. Itu cukup untuk satu instance; deploy tanpa downtime butuh dua replika,
yang memang tidak didukung aplikasi ini.

Menjalankan deploy otomatis dari GitHub Actions (SSH atau SSM) bisa ditambahkan
nanti. Mulai manual dulu: untuk satu server, perintah di atas sudah cukup dan
tidak menyimpan kredensial AWS di GitHub.

## Backup

Yang perlu dicadangkan hanya dua hal:

1. **`.env`**: simpan di password manager tim atau AWS Systems Manager
   Parameter Store (tipe standar gratis).
2. **Volume `csr-data`** (indeks CSR). Cara termurah adalah snapshot EBS terjadwal
   lewat **Amazon Data Lifecycle Manager**: harian, simpan 7. Snapshot bersifat
   inkremental, jadi biayanya hanya beberapa sen per bulan untuk disk sekecil ini.

Indeks CSR dapat dibangun ulang dengan crawl, jadi kehilangannya merepotkan,
bukan bencana. Sertifikat Caddy juga dapat diterbitkan ulang.

## Operasional sehari-hari

Singkat perintahnya dengan alias:

```bash
alias hc='docker compose -f deploy/docker-compose.yml -f deploy/ec2/docker-compose.caddy.yml --env-file .env'
```

| Kebutuhan | Perintah |
| --------- | -------- |
| Status | `hc ps` |
| Log harness (JSON) | `hc logs -f harness` |
| Log crawl harian | `hc logs --since 24h csr-crawler` |
| Crawl manual | `hc run --rm --entrypoint csrctl harness crawl` |
| Restart satu layanan | `hc restart harness` |
| Pemakaian disk | `docker system df` dan `df -h /` |
| Update OS | `sudo apt-get update && sudo apt-get upgrade -y`, lalu reboot di jam sepi |

Pantau dari luar: buat health check gratis (misalnya Route 53 health check atau
UptimeRobot) ke `https://<domain>/healthz`, dengan notifikasi ke email atau
Slack tim. Pasang juga CloudWatch alarm `StatusCheckFailed` dengan aksi
*recover* agar EC2 pulih sendiri bila host AWS bermasalah.

Bila Dependabot membuka PR pembaruan image (Go, SearXNG, Caddy), gabungkan
seperti PR biasa lalu jalankan `deploy/ec2/deploy.sh`. Pantau juga versi SearXNG
yang disematkan di compose.

## Kapan naik ke yang lebih besar

Rincian arsitektur scale-out, perubahan kode yang diperlukan, dan perkiraan
biayanya ada di [scaling.md](scaling.md).

Tetap di satu EC2 sampai salah satu ini benar:

- layanan harus tetap hidup ketika satu mesin mati (butuh HA),
- aplikasi sudah memindahkan percakapan dan indeks ke penyimpanan bersama
  (Postgres atau Redis) dan boleh punya banyak replika,
- ada banyak layanan atau tim yang berbagi infrastruktur.

Saat itu, urutan yang masuk akal dari murah ke mahal: dua EC2 di belakang ALB
dengan penyimpanan bersama, lalu **ECS Fargate**, dan baru Kubernetes bila ada
alasan nyata (tim platform, banyak layanan). Melompat langsung ke Kubernetes
sekarang berarti membayar kompleksitas tanpa mendapat manfaatnya.
