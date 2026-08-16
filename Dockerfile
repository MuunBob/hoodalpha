# syntax=docker/dockerfile:1
FROM golang:1.26-trixie AS build

WORKDIR /src

# Dependencies first so a source-only change reuses the module cache layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=""
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w \
        -X github.com/MuunBob/hoodalpha/internal/observability/buildinfo.Version=${VERSION} \
        -X github.com/MuunBob/hoodalpha/internal/observability/buildinfo.Commit=${COMMIT}" \
      -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
