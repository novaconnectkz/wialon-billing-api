# Wialon Billing API

Backend API для системы биллинга Wialon.

## Технологии
- Go 1.21+
- Gin (HTTP фреймворк)
- GORM (ORM)
- PostgreSQL

## Установка

1. Скопируйте конфигурацию:
```bash
cp config.yaml.example config.yaml
```

2. Настройте `config.yaml` с вашими параметрами подключения к БД и Wialon

3. Запустите сервер:
```bash
go run cmd/server/main.go
```

## Структура

```
├── cmd/server/         # Точка входа
├── internal/
│   ├── handlers/       # HTTP обработчики
│   ├── models/         # Модели данных
│   ├── repository/     # Работа с БД
│   └── services/       # Бизнес-логика
├── fonts/              # Шрифты для PDF
└── config.yaml.example # Пример конфигурации
```

## API Endpoints

### Аутентификация
- `POST /api/auth/request-code` - Запрос кода
- `POST /api/auth/verify-code` - Верификация кода
- `GET /api/auth/me` - Текущий пользователь

### Аккаунты
- `GET /api/accounts` - Список аккаунтов
- `POST /api/accounts/sync` - Синхронизация с Wialon
- `PUT /api/accounts/:id/details` - Обновление реквизитов

### Модули
- `GET /api/modules` - Список модулей
- `POST /api/modules` - Создание модуля
- `PUT /api/modules/:id` - Редактирование

### Счета
- `GET /api/invoices` - Список счетов
- `GET /api/invoices/:id/pdf` - Скачать PDF
- `POST /api/invoices/generate` - Генерация счетов

### Снимки
- `GET /api/snapshots` - Список снимков
- `POST /api/snapshots/date` - Создать снимок за дату
