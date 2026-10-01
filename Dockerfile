# syntax=docker/dockerfile:1

# --- build stage -------------------------------------------------------
# Keep this Go version >= the `go` directive in go.mod: official Go images
# set GOTOOLCHAIN=local, so an older image cannot build this module.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/harness ./cmd/harness

# --- runtime stage -------------------------------------------------------
# Debian slim rather than distroless: reading CSR and sustainability reports
# needs poppler's pdftotext, which depends on shared libraries distroless
# does not ship. To keep the attack surface small: only poppler-utils and CA
# certificates are installed, no shell tools are added, apt metadata is
# removed, and the process runs as an unprivileged user. pdftotext parses
# untrusted PDFs, so the harness also runs it with a timeout and an output cap.
FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends poppler-utils ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --no-create-home --shell /usr/sbin/nologin harness
COPY --from=build /out/harness /usr/local/bin/harness

EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/harness"]
