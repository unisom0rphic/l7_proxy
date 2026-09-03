FROM golang:1.26-alpine AS builder

ENV PORT=8080
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY internal/ ./internal/
COPY cmd/proxy/ ./cmd/proxy/

RUN CGO_ENABLED=0 GOOS=linux go build -o /out/proxy ./cmd/proxy

FROM alpine:3.20

RUN adduser -D appuser
USER appuser

COPY --from=builder /out/proxy proxy

EXPOSE $PORT

CMD ["./proxy"]
