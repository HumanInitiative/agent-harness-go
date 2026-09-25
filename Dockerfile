# syntax=docker/dockerfile:1

# --- build stage -------------------------------------------------------
# Keep this Go version >= the `go` directive in go.mod: official Go images
# set GOTOOLCHAIN=local, so an older image cannot build this module.
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/harness ./cmd/harness

# --- runtime stage -------------------------------------------------------
# distroless/static has no shell and no package manager, and the :nonroot
# tag runs as an unprivileged user. It ships CA certificates (needed to call
# the Gemini API over TLS); timezone data is embedded in the binary itself.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/harness /harness

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/harness"]
