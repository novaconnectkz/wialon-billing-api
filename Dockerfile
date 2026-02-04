FROM golang:1.24-alpine AS builder

WORKDIR /app

# Копируем go.mod и go.sum
COPY go.mod go.sum ./
RUN go mod download

# Копируем исходники
COPY . .

# Собираем бинарник
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server ./cmd/server

# Финальный образ
FROM alpine:3.18

WORKDIR /app

# Копируем бинарник
COPY --from=builder /app/server .
COPY --from=builder /app/config.yaml.example ./config.yaml

# Порт
EXPOSE 8080

# Запуск
CMD ["./server"]
