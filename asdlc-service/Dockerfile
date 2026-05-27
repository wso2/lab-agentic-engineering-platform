FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/asdlc-api ./cmd/asdlc-api

FROM alpine:3.21

# git is still needed because the merged binary owns the repo working-tree
# operations the standalone git-service used to handle. WS0.4 will swap
# shell-git for the GitHub Git Data API and let us drop this dependency.
RUN apk add --no-cache git
RUN addgroup -g 1000 appgroup && adduser -S -u 1000 -G appgroup appuser
RUN mkdir -p /data/repos && chown -R appuser:appgroup /data/repos
# Allow git to operate in bind-mounted directories regardless of ownership
# (on macOS/Colima the host user maps to root inside the VM, but container
# runs as UID 1000).
RUN mkdir -p /home/appuser && \
    printf '[safe]\n\tdirectory = *\n' > /home/appuser/.gitconfig && \
    chown -R appuser:appgroup /home/appuser

WORKDIR /app
COPY --from=builder /app/bin/asdlc-api .

USER appuser

RUN git config --global --add safe.directory '*'

EXPOSE 8080

ENTRYPOINT ["./asdlc-api"]
