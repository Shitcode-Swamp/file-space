# Multi-stage build for the file-space Go API.
# See REQUIREMENTS.md §5.6 and deploy/docker-compose.yml.

FROM golang:1.26-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/api /usr/local/bin/api

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
