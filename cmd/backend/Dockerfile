FROM golang:1.26-alpine AS builder

ENV PORT=8080
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/backend/ ./cmd/backend/

RUN CGO_ENABLED=0 GOOS=linux go build -o /out/backend ./cmd/backend

FROM alpine:3.20

RUN adduser -D appuser
USER appuser

COPY --from=builder /out/backend backend

EXPOSE $PORT

CMD ["./backend"]
