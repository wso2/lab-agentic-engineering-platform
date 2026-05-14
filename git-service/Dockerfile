FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/git-service ./cmd/git-service

FROM alpine:3.21

RUN apk add --no-cache git
RUN addgroup -g 1000 appgroup && adduser -S -u 1000 -G appgroup appuser
RUN mkdir -p /data/repos && chown -R appuser:appgroup /data/repos
# Allow git to operate in bind-mounted directories regardless of ownership
# (on macOS/Colima the host user maps to root inside the VM, but container runs as UID 1000)
RUN mkdir -p /home/appuser && \
    printf '[safe]\n\tdirectory = *\n' > /home/appuser/.gitconfig && \
    chown -R appuser:appgroup /home/appuser

WORKDIR /app
COPY --from=builder /app/bin/git-service .

USER appuser

# Repos under /data/repos are bind-mounted from the host, so safe.directory
# ownership checks can flake under concurrent git ops even when ownership is
# correct. Trust all paths for this container.
RUN git config --global --add safe.directory '*'

EXPOSE 3300

ENTRYPOINT ["./git-service"]
