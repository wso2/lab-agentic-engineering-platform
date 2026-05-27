FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/local-cluster-gateway-proxy .

FROM alpine:3.21
RUN addgroup -g 1000 appgroup && adduser -S -u 1000 -G appgroup appuser
WORKDIR /app
COPY --from=builder /app/bin/local-cluster-gateway-proxy .
USER appuser
EXPOSE 8085
ENTRYPOINT ["./local-cluster-gateway-proxy"]
