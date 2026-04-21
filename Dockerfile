FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /bot cmd/bot/main.go

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata
RUN addgroup -g 1001 app && adduser -D -u 1001 -G app app

COPY --from=builder /bot /bot
COPY migrations /migrations

USER app
ENTRYPOINT ["/bot"]
