FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/asdlc-api ./cmd/asdlc-api

FROM alpine:3.21

RUN addgroup -g 1000 appgroup && adduser -S -u 1000 -G appgroup appuser
# PR 3 of repo-storage-ownership: the BFF no longer owns /data/repos. Repo
# working trees live exclusively on git-service.

WORKDIR /app
COPY --from=builder /app/bin/asdlc-api .

USER 1000

EXPOSE 8080

ENTRYPOINT ["./asdlc-api"]
