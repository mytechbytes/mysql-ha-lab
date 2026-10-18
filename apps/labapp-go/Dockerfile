# syntax=docker/dockerfile:1
# Match the Go version in go.mod (the "go 1.xx" line).
ARG GO_VERSION=1.27

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/labapp ./cmd/labapp

# static binary + CA certs + tzdata, no shell, runs as uid 65532
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/labapp /labapp
EXPOSE 8080
ENTRYPOINT ["/labapp"]