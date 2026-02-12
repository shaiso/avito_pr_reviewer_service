# PR Reviewer Assignment Service

Сервис назначения ревьюеров для Pull Request’ов.  
Тестовое задание Backend.

Сервис:
- управляет командами и пользователями;
- создаёт PR и автоматически назначает до двух ревьюверов;
- позволяет переназначать ревьюверов;
- помечает PR как `MERGED` (идемпотентно);
- отдаёт список PR, где пользователь является ревьювером;
- предоставляет простой эндпоинт статистики.

## Стек
- Язык: **Go**
- HTTP: **net/http**
- БД: **PostgreSQL 16**
- Драйвер БД: **github.com/jackc/pgx/v5/stdlib**
- Миграции: простой SQL (**migrations/0001_init.sql**)
- Сборка/запуск: **Docker**, **docker compose**, **Makefile**
- Линтер: **golangci-lint**
- Нагрузочное тестирование: **k6**

## Быстрый старт

Требования:

- Docker + docker compose
- Make

### 1. Сборка образа

```
make build
```

### 2. Запуск сервиса

```
make up
```

Будет поднято:

- pr_db — Postgres 16 (порт хоста 55432 → 5432);
- pr_migrate — одноразовый контейнер, применяющий migrations/0001_init.sql;
- pr_app — сервис на Go (порт 8080).

### 3. Проверка health-check:

```
curl http://localhost:8080/health
```

### 4. Остановка

```
make down
```

## Конфигурация

Сервис читает конфигурацию из env:
- `DB_DSN` — строка подключения к Postgres (в [docker-compose.yml](docker-compose.yml):`postgres://pr_user:pr_pass@db:5432/pr_service?sslmode=disable`)
- `ADMIN_TOKEN` — токен для админских запросов (по умолчанию в [docker-compose.yml](docker-compose.yml): `supersecretadmin`)

Админские методы требуют заголовок:
```
X-Admin-Token: <ADMIN_TOKEN>
```

## Миграции

Файл: [migrations/0001_init.sql](migrations/0001_init.sql)

Создаёт таблицы:
- `teams(team_name)`
- `users(user_id, username, is_active, team_name)`
- `pull_requests(pull_request_id, pull_request_name, author_id, status, need_more_reviewers, created_at, merged_at)`
- `pr_reviewers(pull_request_id, user_id)`

И индексы: 
- `idx_users_team_active(team_name, is_active)` — для быстрого поиска кандидатов в ревьюверы;
- `idx_pr_reviewers_user(user_id)` — для выборки PR по ревьюверу.

Миграции автоматически применяются контейнером **pr_migrate** при `docker compose up`.

## Краткий обзор API

Формат ошибок — по OpenAPI:

```aiignore
{
  "error": {
    "code": "NOT_FOUND",
    "message": "resource not found"
  }
}
```

#### 1. Команды
**POST /team/add** 

Создать команду с участниками (создаёт/обновляет пользователей).

- Body (Team):
```
{
  "team_name": "backend",
  "members": [
    { "user_id": "u1", "username": "Alice", "is_active": true },
    { "user_id": "u2", "username": "Bob",   "is_active": true }
  ]
}
```
- Если команда уже существует — `TEAM_EXISTS (400)`.
- Пользователи **upsert**’ятся по `user_id` (обновляются `username`, `is_active`, `team_name`).

Пример: 
```
curl -X POST http://localhost:8080/team/add \
  -H "Content-Type: application/json" \
  -d '{
    "team_name": "backend",
    "members": [
      { "user_id": "u1", "username": "Alice",   "is_active": true },
      { "user_id": "u2", "username": "Bob",     "is_active": true },
      { "user_id": "u3", "username": "Charlie", "is_active": true }
    ]
  }'
```

**GET /team/get?team_name=backend**

Получить команду с участниками.
- `200` — объект команды;
- `404 NOT_FOUND` — команда не найдена.

#### 2. Пользователи

**POST /users/setIsActive** (админ)

Установить флаг активности пользователя.

- Требует заголовок: `X-Admin-Token`.
- Body:
```
{
  "user_id": "u2",
  "is_active": false
}
```
- `200` — возвращает пользователя;
- `404 NOT_FOUND` — пользователь не найден.

Пример: 
```
curl -X POST http://localhost:8080/users/setIsActive \
  -H "Content-Type: application/json" \
  -H "X-Admin-Token: supersecretadmin" \
  -d '{
    "user_id": "u2",
    "is_active": false
  }'
```

**GET /users/getReview?user_id=u2**
Получить список PR, где пользователь назначен ревьювером.
- Возвращает:
```
{
  "user_id": "u2",
  "pull_requests": [
    {
      "pull_request_id": "pr-1",
      "pull_request_name": "Add search",
      "author_id": "u1",
      "status": "OPEN"
    }
  ]
}
```

- Допущение:
если пользователь не существует, сервис возвращает `404 NOT_FOUND`. В OpenAPI для этого метода `404` не указан, это моё осознанное решение (описано в разделе «Допущения»).

#### 3. Pull Request’ы

**POST /pullRequest/create** (админ)

Создать PR и автоматически назначить ревьюверов.

- Требует `X-Admin-Token`.
- Body:
```
{
  "pull_request_id": "pr-1",
  "pull_request_name": "Add search",
  "author_id": "u1"
}
```
- Логика:
  - Автор должен существовать и принадлежать команде.
  - Выбираются до двух активных пользователей из команды автора:
    - `is_active = true`
    - `user_id != author_id`
  - Выбор случайный (`ORDER BY random() LIMIT 2`).
  - Если кандидатов < 2 → назначается 0/1 ревьювер.
  - `need_more_reviewers =` (кол-во ревьюверов < 2).
- Ошибки:
  - `404 NOT_FOUND` — автор не найден;
  - `409 PR_EXISTS` — PR с таким `pull_request_id` уже есть.

Пример:
```
curl -X POST http://localhost:8080/pullRequest/create \
  -H "Content-Type: application/json" \
  -H "X-Admin-Token: supersecretadmin" \
  -d '{
    "pull_request_id": "pr-1",
    "pull_request_name": "Add search",
    "author_id": "u1"
  }'
```

Ответ:
```
{
  "pr": {
    "pull_request_id": "pr-1",
    "pull_request_name": "Add search",
    "author_id": "u1",
    "status": "OPEN",
    "assigned_reviewers": ["u2", "u3"],
    "needMoreReviewers": false,
    "createdAt": "2025-11-14T10:00:00Z",
    "mergedAt": null
  }
}
```

Дополнение к OpenAPI: поле `needMoreReviewers` отсутствует в спецификации, но есть в текстовом описании задачи.  Я добавил его в модель и ответы API.

**POST /pullRequest/merge** (админ, идемпотентный)

Пометить PR как MERGED.

- Требует `X-Admin-Token`.
- Body:
```
{
  "pull_request_id": "pr-1"
}
```
- Если PR в статусе `OPEN`:
  - обновляется `status = MERGED`,
  - проставляется `merged_at = NOW()`.
- Если PR уже MERGED:
  - состояние не меняется,
  - просто возвращается актуальная версия.
- Всегда возвращает PR в статусе MERGED.
- Ошибки:
  - `404 NOT_FOUND` — PR не найден.

Пример: 
```
curl -X POST http://localhost:8080/pullRequest/merge \
  -H "Content-Type: application/json" \
  -H "X-Admin-Token: supersecretadmin" \
  -d '{"pull_request_id": "pr-1"}'
```

**POST /pullRequest/reassign** (админ)

Переназначить конкретного ревьювера на другого из его команды.
- Требует X-Admin-Token.
- Body:
```
{
  "pull_request_id": "pr-1",
  "old_user_id": "u2"
}
```

- Допущение: в OpenAPI в примере поле называется old_reviewer_id, я использую old_user_id, чтобы сохранить единый нейминг по всему API.
- Логика:
  - PR загружается с `FOR UPDATE`.
  - Если `status = MERGED` → `409 PR_MERGED`.
  - Проверяется, что `old_user_id` существует.
  - Проверяется, что этот пользователь был ревьювером данного PR → иначе `409 NOT_ASSIGNED`.
  - Определяется команда `old_user_id`.
  - Подбирается кандидат из этой команды:
    - `is_active = true`,
    - не автор PR,
    - не `old_user_id`,
    - не второй текущий ревьювер.
  - Если кандидата нет → `409 NO_CANDIDATE`.
  - В `pr_reviewers` происходит замена `old_user_id` на нового.
  - Обновляется `need_more_reviewers` (на случай расширения логики).
Ответ:
```
{
  "pr": { ...обновлённый PR... },
  "replaced_by": "u5"
}
```
Ошибки: 
- `404 NOT_FOUND` — PR или пользователь не найден;
- `409 PR_MERGED` — нельзя менять после merge;
- `409 NOT_ASSIGNED` — пользователь не был ревьювером данного PR;
- `409 NO_CANDIDATE` — нет доступных активных кандидатов в команде.

#### 4. Эндпоинт статистики

Дополнительное задание: простой эндпоинт статистики по ревьюверам.

**GET /stats/reviewers** (админ)

Возвращает количество назначений по пользователям.
- Требует `X-Admin-Token`.
- Считает количество записей в` pr_reviewers` на каждого пользователя (`LEFT JOIN`, чтобы включать и тех, у кого ещё нет назначений).

Пример ответа:
```
{
  "items": [
    { "user_id": "u2", "username": "Bob",     "assignments": 15 },
    { "user_id": "u3", "username": "Charlie", "assignments": 7 },
    { "user_id": "u1", "username": "Alice",   "assignments": 0 }
  ]
}
```

## Линтер (golangci-lint)

Конфигурация: [.golangci.yml](.golangci.yml) в корне проекта.

Используем минимальный набор линтеров:
- govet
- staticcheck
- unused
- errcheck
- revive

Запуск:

```
golangci-lint run ./...
```

## Нагрузочное тестирование (k6)
Дополнительное задание: нагрузочное тестирование сервиса.

Скрипт:[ loadtest/k6-pr-create.js](loadtest/k6-pr-create.js)

Запуск:

1. Поднять сервис:
```
make up
```
2. Убедиться, что команда и пользователь u1 созданы (один раз):
```
curl -X POST http://localhost:8080/team/add \
  -H "Content-Type: application/json" \
  -d '{
    "team_name": "backend",
    "members": [
      { "user_id": "u1", "username": "Alice",   "is_active": true },
      { "user_id": "u2", "username": "Bob",     "is_active": true },
      { "user_id": "u3", "username": "Charlie", "is_active": true }
    ]
  }'
```
3. Запустить k6:
```
k6 run loadtest/k6-pr-create.js
```

**Результаты (локально)**

На моей машине k6 выдал:
- 2816 запросов за 30 секунд (~93.6 RPS);
- http_req_failed: 0.00% — все ответы либо 201, либо 409 (что считается успехом в сценарии);
- http_req_duration:
  - avg ≈ 5.95 ms
  - p(90) ≈ 13.2 ms
  - p(95) ≈ 19.01 ms
  - max ≈ 194.65 ms

Таким образом:
- SLI по времени ответа 300 ms выполняется с большим запасом;
- SLI по успешности 99.9% также выдерживается (ошибок нет).

## Допущения и расхождения с OpenAPI
1. Поле `needMoreReviewers`
- В текстовом описании задачи есть флаг `needMoreReviewers`, в OpenAPI — нет.
- Я добавил его в модель `PullRequest` и возвращаю во всех ответах с PR.
2. **/users/getReview** — поведение при несуществующем пользователе
- В OpenAPI для этого метода указана только успешная `200`.
- Я посчитал более логичным вернуть `404 NOT_FOUND`, если пользователь не существует.
- Если пользователь существует, но у него нет PR — возвращается `{ user_id, pull_requests: [] }`.
3. Нейминг поля в **pullRequest/reassign**
- В примере для **/pullRequest/reassign** используется `old_reviewer_id`.
- В реализации использую `old_user_id`, чтобы унифицировать нейминг по всему API (`user_id` везде).
4. Авторизация
- В спецификации есть` AdminToken / UserToken`, но детали токенов не раскрыты.
- Для простоты использую заголовок `X-Admin-Token` с одним статичным токеном из env (`ADMIN_TOKEN`).
- Авторизация пользователей по `UserToken` не реализована, но эндпоинты, потенциально доступные пользователю, оставлены без проверки токена (например, **/users/getReview**).
