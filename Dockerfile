FROM golang:1.24.6 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o pr-service ./cmd/server

FROM gcr.io/distroless/base-debian12

WORKDIR /app

COPY --from=builder /app/pr-service /app/pr-service
COPY --from=builder /app/migrations /app/migrations

ENV PORT=8080

EXPOSE 8080

ENTRYPOINT ["/app/pr-service"]