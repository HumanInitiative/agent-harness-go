# Migrasi ke Kubernetes

Dokumen ini adalah peta jalan untuk memindahkan harness dari Docker Compose di
satu EC2 ([deployment-ec2.md](deployment-ec2.md)) ke Kubernetes. Ini bukan
langkah yang disarankan sekarang; baca dulu
[Kapan Kubernetes layak](#kapan-kubernetes-layak).

Manifest di bawah adalah **titik awal yang belum diuji** pada klaster nyata.
Satu-satunya yang sudah dicoba adalah
[`csr-crawl-cronjob.yaml`](../deploy/kubernetes/csr-crawl-cronjob.yaml) (di
kind, lihat komentar di file itu). Uji semuanya di klaster percobaan sebelum
dipakai.

- [Kapan Kubernetes layak](#kapan-kubernetes-layak)
- [Prasyarat: aplikasi harus stateless](#prasyarat-aplikasi-harus-stateless)
- [Pilihan platform](#pilihan-platform)
- [Pemetaan dari Compose ke Kubernetes](#pemetaan-dari-compose-ke-kubernetes)
- [Manifest](#manifest)
- [Pipeline dan deploy](#pipeline-dan-deploy)
- [Observabilitas dan keamanan](#observabilitas-dan-keamanan)
- [Rencana migrasi bertahap](#rencana-migrasi-bertahap)
- [Biaya dan beban kerja tim](#biaya-dan-beban-kerja-tim)

## Kapan Kubernetes layak

Kubernetes menyelesaikan masalah yang tidak dimiliki setup satu-EC2 sekarang.
Pertimbangkan hanya bila **dua atau lebih** hal ini benar:

- ada banyak layanan (bukan hanya harness) yang akan berbagi platform,
- ada orang yang bertugas merawat klaster (upgrade, keamanan, observabilitas),
- butuh replika banyak dengan scale otomatis dan deploy tanpa downtime,
- ada kebutuhan kepatuhan atau multi-tim yang menuntut isolasi namespace dan
  kebijakan jaringan,
- tim sudah mahir Kubernetes, atau memang ingin berinvestasi di sana.

Bila hanya untuk satu layanan ini, ECS Fargate atau Auto Scaling Group di
[scaling.md](scaling.md) hampir selalu lebih murah dan lebih sederhana.

## Prasyarat: aplikasi harus stateless

Kubernetes tidak memperbaiki state yang terikat pada proses. Selesaikan dulu
perubahan di [scaling.md](scaling.md#perubahan-kode-yang-diperlukan):

| Perubahan | Mengapa wajib |
| --------- | ------------- |
| `ConversationStore` berbasis Redis | Pod bisa dihapus kapan saja; riwayat tidak boleh ikut hilang |
| `TurnGuard` terdistribusi | Dua pod tidak boleh memproses percakapan yang sama bersamaan |
| Rate limit bersama (atau diterima N kali lipat) | Replika menggandakan batas |
| Indeks CSR di Postgres, atau Litestream ke S3 | Volume SQLite tidak bisa dipakai bersama banyak node |

Tanpa itu, jalankan **tepat satu replika**. Kubernetes tetap bisa berguna
(restart otomatis, deploy terkelola), tetapi tidak ada scale out, dan satu
replika berarti ada downtime kecil setiap kali pod diganti
(`strategy: Recreate`, lihat catatan di bawah).

## Pilihan platform

| Opsi | Cocok untuk | Catatan |
| ---- | ----------- | ------- |
| **k3s di 1 EC2** | Belajar, tahap awal, biaya minimum | Tanpa HA control plane. Satu node, jadi tidak lebih tersedia daripada Compose. Biaya hampir sama dengan sekarang |
| **EKS + node terkelola** | Produksi di AWS | Control plane sekitar US$73 per bulan, belum termasuk node. Upgrade versi Kubernetes berkala (tiap beberapa bulan) |
| **EKS Auto Mode / Fargate** | Tim kecil yang ingin mengurangi tugas node | Lebih mahal per vCPU, lebih sedikit yang dirawat |

Saran: jika memutuskan masuk, mulai di **k3s** untuk belajar dan uji manifest,
lalu pindah ke **EKS** hanya ketika syarat di atas benar-benar terpenuhi. Jangan
menjalankan klaster Kubernetes buatan sendiri dengan banyak node di EC2
(kubeadm): itu menanggung semua beban operasional tanpa manfaat layanan terkelola.

## Pemetaan dari Compose ke Kubernetes

| Compose ([`deploy/docker-compose.yml`](../deploy/docker-compose.yml)) | Kubernetes |
| ----- | ----- |
| `harness` | `Deployment` + `Service` (ClusterIP) |
| `caddy` / TLS | `Ingress` (AWS Load Balancer Controller + sertifikat ACM, atau ingress-nginx + cert-manager) |
| `csr-crawler` (`csrctl schedule`) | `CronJob` ([sudah ada](../deploy/kubernetes/csr-crawl-cronjob.yaml)); jadwal ditangani Kubernetes, jadi `schedule` tidak dipakai |
| `searxng` | `Deployment` + `Service`, atau sidecar di pod harness |
| `.env` | `Secret` (`agent-harness-env`) berisi variabel; idealnya disinkronkan dari AWS Secrets Manager |
| `csr-data` volume | Tidak lagi dipakai harness bila indeks pindah ke Postgres/Litestream |
| `restart: unless-stopped` | Bawaan `Deployment` |
| `stop_grace_period: 80s` | `terminationGracePeriodSeconds: 80` (harus > `SHUTDOWN_TIMEOUT_SECONDS`) |
| `/healthz`, `/readyz` | `livenessProbe`, `readinessProbe` |

## Manifest

Semua memakai namespace `agent-harness`, dan Secret `agent-harness-env` seperti
asumsi di file CronJob.

### Harness

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: harness
  namespace: agent-harness
  labels:
    app.kubernetes.io/name: agent-harness
    app.kubernetes.io/component: server   # dicocokkan oleh affinity CronJob
spec:
  replicas: 2
  strategy:
    type: RollingUpdate
    rollingUpdate: { maxUnavailable: 0, maxSurge: 1 }
  selector:
    matchLabels:
      app.kubernetes.io/name: agent-harness
      app.kubernetes.io/component: server
  template:
    metadata:
      labels:
        app.kubernetes.io/name: agent-harness
        app.kubernetes.io/component: server
    spec:
      # Harus lebih besar dari SHUTDOWN_TIMEOUT_SECONDS (default 70).
      terminationGracePeriodSeconds: 80
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        seccompProfile: { type: RuntimeDefault }
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: topology.kubernetes.io/zone
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              app.kubernetes.io/component: server
      containers:
        - name: harness
          image: <ECR_URI>/agent-harness-go:<sha>   # selalu sha, bukan latest
          ports: [{ name: http, containerPort: 8080 }]
          envFrom:
            - secretRef: { name: agent-harness-env }
          env:
            - { name: WEB_TOOLS_ENABLED, value: "true" }
            - { name: CSR_TOOLS_ENABLED, value: "true" }
            - { name: SEARXNG_URL, value: "http://searxng:8080" }
          livenessProbe:
            httpGet: { path: /healthz, port: http }
            periodSeconds: 10
          readinessProbe:
            httpGet: { path: /readyz, port: http }
            periodSeconds: 5
          resources:
            requests: { cpu: 250m, memory: 256Mi }
            limits: { memory: 768Mi }
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
          volumeMounts:
            - { name: tmp, mountPath: /tmp }   # pdftotext menulis di sini
      volumes:
        - name: tmp
          emptyDir: { sizeLimit: 1Gi }
---
apiVersion: v1
kind: Service
metadata: { name: harness, namespace: agent-harness }
spec:
  selector:
    app.kubernetes.io/name: agent-harness
    app.kubernetes.io/component: server
  ports: [{ name: http, port: 80, targetPort: http }]
```

Catatan penting:

- **Satu replika sebelum state dipindah**: ganti menjadi `replicas: 1` dan
  `strategy: Recreate`. Pod baru tidak boleh berjalan berdampingan dengan pod
  lama, atau rate limit dan percakapan terpecah. Konsekuensinya ada downtime
  singkat per deploy.
- **Jangan lupa mengisi `API_KEYS`, `GEMINI_API_KEY`, `SEARXNG_SECRET`** di
  Secret; aplikasi berhenti saat start bila ada yang kurang.
- Aplikasi menulis ke `/tmp` untuk PDF, karena itu `readOnlyRootFilesystem`
  dipadukan dengan `emptyDir`, persis seperti pada file CronJob.

### Scale otomatis dan ketersediaan

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: { name: harness, namespace: agent-harness }
spec:
  scaleTargetRef: { apiVersion: apps/v1, kind: Deployment, name: harness }
  minReplicas: 2
  maxReplicas: 6
  metrics:
    - type: Resource
      resource:
        name: cpu
        target: { type: Utilization, averageUtilization: 60 }
  behavior:
    scaleDown:
      stabilizationWindowSeconds: 600   # giliran model bisa lama; turunkan pelan
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: { name: harness, namespace: agent-harness }
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app.kubernetes.io/component: server
```

HPA berbasis CPU kurang pas untuk beban yang sebagian besar menunggu I/O. Bila
perlu, gunakan metrik jumlah request per pod (KEDA, atau metrik ALB lewat
adapter). Batas pertama tetap kuota Gemini, bukan jumlah pod.

### SearXNG

Jalankan sebagai `Deployment` kecil (1 sampai 2 replika) dengan `Service`
bernama `searxng` di port 8080, memakai image yang sama dengan Compose
(`searxng/searxng:<tag-terpin>`) dan `settings.yml` dari
[`deploy/searxng/settings.yml`](../deploy/searxng/settings.yml) lewat `ConfigMap`.
Karena itu `ClusterIP` saja, tanpa Ingress. Tambahkan `NetworkPolicy` agar hanya
pod harness dan CronJob yang boleh mengaksesnya.

### Ingress dan TLS

Di EKS: AWS Load Balancer Controller membuat ALB dari `Ingress`, dengan
sertifikat ACM.

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: harness
  namespace: agent-harness
  annotations:
    alb.ingress.kubernetes.io/scheme: internet-facing
    alb.ingress.kubernetes.io/target-type: ip
    alb.ingress.kubernetes.io/certificate-arn: <ARN sertifikat ACM>
    alb.ingress.kubernetes.io/healthcheck-path: /readyz
    alb.ingress.kubernetes.io/target-group-attributes: deregistration_delay.timeout_seconds=80
spec:
  ingressClassName: alb
  rules:
    - host: harness.humaninitiative.org
      http:
        paths:
          - { path: /, pathType: Prefix, backend: { service: { name: harness, port: { name: http } } } }
```

Di k3s: gunakan Traefik bawaan atau ingress-nginx dengan cert-manager.

### Crawler harian

Pakai [`csr-crawl-cronjob.yaml`](../deploy/kubernetes/csr-crawl-cronjob.yaml)
apa adanya sebagai langkah awal. File itu mengunci pod ke node yang sama dengan
harness karena indeks SQLite ada di volume `ReadWriteOnce`. Setelah indeks pindah
ke Postgres atau Litestream, hapus `podAffinity` dan volume `csr-data`, lalu
tambahkan koneksi database ke Secret. `concurrencyPolicy: Forbid` dan kunci
crawl di database menjaga agar hanya satu crawl yang berjalan.

## Pipeline dan deploy

1. **CI** (sudah ada) tetap menjadi gerbang. Tambahkan job yang membangun image,
   menandainya dengan SHA commit, dan mendorongnya ke **ECR**. Gunakan OIDC GitHub
   ke AWS, bukan access key statis.
2. **Deploy** lewat GitOps (Argo CD atau Flux) yang memantau folder manifest di
   repo, atau `kubectl set image` / `helm upgrade` dari pipeline bila ingin
   lebih sederhana. GitOps unggul karena status klaster selalu sama dengan Git,
   dan rollback cukup `git revert`.
3. **Pengemasan.** Mulai dengan Kustomize (`base/` + `overlays/prod`), yang
   cukup untuk satu layanan. Helm baru berguna bila chart dipakai di banyak
   lingkungan.
4. **Rollback**: `kubectl rollout undo deployment/harness`, atau revert di Git.

## Observabilitas dan keamanan

- **Log**: aplikasi sudah mengeluarkan JSON ke stdout. Kirim ke CloudWatch
  (Fluent Bit) atau Loki.
- **Metrik**: metrics-server untuk HPA; Prometheus + Grafana atau Amazon Managed
  Prometheus bila perlu dasbor. Aplikasi belum mengekspos endpoint Prometheus;
  tambahkan bila dibutuhkan.
- **Alarm**: pod tidak Ready, restart berulang, CronJob gagal, 429/503 dari
  model, sertifikat hampir kedaluwarsa.
- **Rahasia**: External Secrets Operator yang menarik dari AWS Secrets Manager,
  bukan `Secret` yang ditulis tangan di Git.
- **Jaringan**: `NetworkPolicy` default-deny di namespace, lalu izinkan hanya
  jalur yang perlu (Ingress → harness, harness → SearXNG/Redis/Postgres,
  keluar ke internet untuk Gemini dan web).
- **Akses**: RBAC berdasarkan IAM/SSO, tanpa kubeconfig bersama. Pod Security
  Standards `restricted` sudah dipenuhi oleh `securityContext` di atas.
- **Upgrade**: jadwalkan upgrade versi Kubernetes dan node secara berkala; EKS
  hanya mendukung tiap versi untuk waktu terbatas.

## Rencana migrasi bertahap

1. **Selesaikan prasyarat stateless** ([scaling.md](scaling.md)), dengan mode
   satu-instance tetap berfungsi.
2. **Pindahkan build ke ECR** dan tandai image dengan SHA. Ini juga berguna
   untuk Compose.
3. **Uji di klaster lokal atau k3s**: terapkan manifest, jalankan uji beban dan
   uji "matikan pod saat ada request berjalan" untuk memastikan
   `terminationGracePeriodSeconds` dan shutdown graceful bekerja.
4. **Jalankan paralel**: klaster baru melayani domain uji (`harness-k8s...`),
   Compose tetap melayani produksi.
5. **Pindahkan lalu lintas** dengan menurunkan TTL DNS lebih dulu, lalu
   mengalihkan domain produksi (atau memakai Route 53 weighted record, 10% →
   50% → 100%). Pantau error dan latensi di tiap langkah.
6. **Pertahankan Compose siap** selama beberapa minggu sebagai jalan kembali;
   matikan hanya setelah klaster stabil.
7. **Pindahkan crawler** terakhir, dan pastikan hanya satu sistem (Compose atau
   CronJob) yang menjalankannya.

## Biaya dan beban kerja tim

Kasar, per bulan di Singapura; verifikasi di AWS Pricing Calculator.

| Setup | Kira-kira |
| ----- | --------- |
| Compose di 1 EC2 (sekarang) | ~US$18 |
| k3s di 1 EC2 | ~US$18 sampai 30 |
| EKS: control plane + 2 node `t4g.medium` + ALB + Redis + Postgres | ~US$180 sampai 250 |

Di luar biaya tagihan, perkirakan waktu orang: upgrade klaster, patch node,
merawat add-on (ingress, cert-manager, external-secrets, observabilitas), dan
on-call. Untuk satu layanan, biaya waktu itu biasanya lebih besar daripada
tagihan AWS. Itulah alasan utama menunda Kubernetes sampai alasan di
[bagian pertama](#kapan-kubernetes-layak) benar-benar nyata.
