FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY go.mod ./
RUN go mod tidy

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o database-service ./cmd/database-service

FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /build/database-service .

EXPOSE 3500

CMD ["./database-service"]
