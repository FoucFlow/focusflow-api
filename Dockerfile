# syntax=docker/dockerfile:1
# FocusFlow API: one static Go binary in a distroless image (~20 MB).
# No .env or secrets are baked in: the VPS passes /opt/focusflow/.env at runtime.

FROM golang:1.25-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/focusflow-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/focusflow-api /focusflow-api
USER nonroot:nonroot
EXPOSE 8091
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/focusflow-api", "healthcheck"]
ENTRYPOINT ["/focusflow-api"]
