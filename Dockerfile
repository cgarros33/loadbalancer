FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -mod=vendor -o loadbalancer ./cmd/server

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/loadbalancer .
EXPOSE 8080
CMD ["./loadbalancer"]
