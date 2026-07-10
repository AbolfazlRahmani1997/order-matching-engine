FROM golang:1.22-alpine AS builder
ENV GOPROXY=https://mirror-go.runflare.com,direct
ENV GOSUMDB=off
WORKDIR /app
COPY go.mod go.sum ./
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 go build -o /matching-engine ./cmd

FROM alpine:3.19
COPY --from=builder /matching-engine /usr/local/bin/matching-engine
COPY config.yaml /etc/matching-engine/config.yaml

EXPOSE 8082

CMD ["matching-engine", "/etc/matching-engine/config.yaml"]
