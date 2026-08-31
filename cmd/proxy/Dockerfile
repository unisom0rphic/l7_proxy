FROM golang:1.26-alpine AS builder

ENV PORT=8080

WORKDIR /app

COPY go.mod go.su[m] ./

RUN if [ -f go.sum ]; then go mod download; else go mod tidy; fi

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o main .

FROM alpine:3.20

RUN adduser -D appuser
USER appuser

WORKDIR /app

COPY --from=builder /app/main .

EXPOSE $PORT

CMD ["./main"]
