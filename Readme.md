# Времянка — временные короткие ссылки (ветка `temp-links`)

Анонимный сервис **временных** коротких ссылок: создаёт короткий код с ограниченным сроком жизни, делает редирект на оригинальный URL и собирает статистику переходов с геолокацией. Просроченные ссылки отдают `410 Gone`, а фоновая чистка удаляет их из базы. Разворачивается одной командой через Docker Compose (nginx + FastAPI + Redis + SQLite).

![Интерфейс приложения](app_screen.png)

> Подробная архитектура базовой версии — в [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Отличия ветки `temp-links` описаны ниже.

## Требования (вариант №3 — временные короткие ссылки)
1. Создание короткой ссылки по длинному URL **с ограниченным сроком жизни**
2. Редирект по короткой ссылке на оригинальный URL
3. Просроченная ссылка больше не работает: `410 Gone`
4. Автоматическая очистка просроченных ссылок и их статистики
5. Сбор аналитики: количество переходов, геолокация
6. Без регистрации

## Параметры временных ссылок

| Параметр | Значение |
|---|---|
| Варианты срока на фронте | 10 минут, 1 час, 24 часа, 7 дней |
| По умолчанию | 1 час (`DEFAULT_TTL_SECONDS=3600`) |
| Границы TTL | от 60 сек (`MIN_TTL_SECONDS`) до 7 дней (`MAX_TTL_SECONDS`) |
| Хранение | колонка `expires_at` (UTC, `YYYY-MM-DD HH:MM:SS`) + индекс `idx_links_expires` |
| Просроченная ссылка | `410 Gone` с сообщением «Срок жизни ссылки истёк» |
| Кэш редиректа | живёт не дольше самой ссылки: `min(CACHE_TTL_SECONDS, до истечения)` |
| Очистка | фоновая задача в lifespan: каждые `CLEANUP_INTERVAL_SECONDS` (300 сек) `DELETE FROM links WHERE expires_at < now` — статистика удаляется каскадом (`ON DELETE CASCADE`) |
| Формат времени | всё в UTC, сравнение строк корректно благодаря фиксированному формату |

## Интерфейс
Тёплый жёлтый минимализм (#FFF9EC фон, #F5A623 акцент), карточки вместо окон, чипы выбора срока жизни. Тосты вместо alert'ов. Две скрытые пасхалки: 10 кликов по логотипу превращают 🍋 в 🧅 («временные ссылки как лук — снимешь слой, прослезись, что истекла»), код Konami включает режим самоуничтожения (шутка — ссылки и так временные).

## Технологический стек

| Категория            | Выбор                                        | Комментарий                                              |
| -------------------- | -------------------------------------------- | -------------------------------------------------------- |
| Backend              | Python 3.12 + FastAPI (uvicorn)              | async, pydantic-валидация, автодокументация              |
| База данных          | SQLite (aiosqlite, режим WAL)                | файл на docker-томе, без отдельного сервера              |
| Кэш                  | Redis 7 (`redis-py` async, AOF)              | кэш редиректов и гео, TTL не превышает срок ссылки       |
| Reverse proxy        | nginx 1.27                                   | единый вход :80, раздача статики, X-Forwarded-For        |
| Frontend             | Статические HTML/CSS/JS без сборки           | ванильный JS, тёплый жёлтый минимализм                   |
| Базовые образы Docker| Alpine Linux                                 | python:3.12-alpine, nginx:1.27-alpine, redis:7-alpine    |
| Очередь AMQP         | ❌ не используется                           | чистка работает циклом в lifespan, очередь избыточна     |

## API endpoints

| Endpoint                          | Метод | Описание                                  |
| --------------------------------- | ----- | ----------------------------------------- |
| `/api/v1/links`                   | POST  | Создать временную ссылку (`ttl_seconds` опционален, 60..604800) |
| `/{short_code}`                   | GET   | Редирект на оригинальный URL; просрочено → `410 Gone` |
| `/api/v1/links/{short_code}/stats`| GET   | Получить статистику (содержит `expires_at`) |
| `/api/v1/links/delete/{short_code}` | DELETE | Удалить ссылку досрочно                 |
| `/api/v1/health`                  | GET   | Healthcheck (db + redis)                  |

## Схема базы данных

```sql
CREATE TABLE IF NOT EXISTS links (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    short_code   TEXT    NOT NULL UNIQUE,
    original_url TEXT    NOT NULL,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    expires_at   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_links_short_code ON links(short_code);
CREATE INDEX IF NOT EXISTS idx_links_expires    ON links(expires_at);

CREATE TABLE IF NOT EXISTS analytics (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    link_id    INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    clicked_at TEXT    NOT NULL DEFAULT (datetime('now')),
    ip_address TEXT,
    country    VARCHAR(2)
);
```

---

## Запуск

### Предварительные требования
* [Docker Desktop](https://www.docker.com/products/docker-desktop/) для Windows/macOS (или Docker Engine + Docker Compose v2 на Linux)
* Свободный порт **80** на хосте

### Запуск
```cmd
docker compose up -d --build
```

После старта приложение доступно по адресу: **http://localhost**

Проверить готовность (все сервисы должны быть `healthy`):
```cmd
docker compose ps
```

### Остановка
```cmd
docker compose down
```
Данные (SQLite-файл и AOF-кэш Redis) сохраняются в docker-томах `sqlite-data` и `redis-data` и переживают пересборку.

### Локальный запуск без Docker (для разработки)
```cmd
python -m venv .venv
.venv\Scripts\activate
pip install -r requirements.txt
set DATABASE_PATH=./local.db
set REDIS_URL=redis://localhost:6379/0
uvicorn app.main:app --reload
```

## Проверка API (smoke-тест)

**1. Создать ссылку на 10 минут** — `POST /api/v1/links` → 201:
```cmd
curl -s -X POST http://localhost/api/v1/links -H "Content-Type: application/json" -d "{\"original_url\":\"https://example.com/some/very/long/path\",\"ttl_seconds\":600}"
```
```json
{"short_code":"bLQxnZM","short_url":"http://localhost/bLQxnZM","original_url":"https://example.com/some/very/long/path","expires_at":"2026-09-15 12:40:00"}
```

**2. Перейти** — `GET /bLQxnZM` → 302 на оригинальный URL:
```cmd
curl -s -i http://localhost/bLQxnZM
```

**3. Статистика** — `GET /api/v1/links/bLQxnZM/stats` → 200:
```cmd
curl -s http://localhost/api/v1/links/bLQxnZM/stats
```
```json
{"short_code":"bLQxnZM","original_url":"https://example.com/some/very/long/path","created_at":"2026-09-15 11:30:00","expires_at":"2026-09-15 11:40:00","clicks":3,"countries":[{"country":"local","count":3}]}
```

**4. Просроченная ссылка** — после истечения `GET /bLQxnZM` → **410 Gone**:
```cmd
curl -s -o nul -w "%{http_code}" http://localhost/bLQxnZM
```
```
410
```

**5. Досрочное удаление** — `DELETE /api/v1/links/delete/{short_code}` → 200, повторный вызов → 404:
```cmd
curl -s -i -X DELETE http://localhost/api/v1/links/delete/bLQxnZM
```
```json
{"short_code":"bLQxnZM","deleted":true}
```

**Негативные сценарии:**
```cmd
:: TTL вне диапазона -> 400
curl -s -i -X POST http://localhost/api/v1/links -H "Content-Type: application/json" -d "{\"original_url\":\"https://example.com\",\"ttl_seconds\":30}"

:: не-URL -> 422 (pydantic)
curl -s -i -X POST http://localhost/api/v1/links -H "Content-Type: application/json" -d "{\"original_url\":\"not-a-url\"}"
```

## Стек и структура проекта

| Сервис | Образ | Порт | Назначение |
|---|---|---|---|
| nginx | nginx:1.27-alpine | **:80** (единственный вход снаружи) | reverse proxy, раздача статики `static/` |
| app | python:3.12-alpine | :8000 (только внутренняя сеть) | FastAPI: создание/редирект/статистика/удаление/чистка |
| redis | redis:7-alpine | :6379 (только внутренняя сеть) | кэш редиректов и гео-данных (AOF, LRU 128 MB) |
| — | — | том `sqlite-data` | база SQLite `/data/urlshortener.db` |

```
temp-links-shortener/
├── app/                     # FastAPI-приложение
│   ├── __init__.py          # версия (2.0.0-ttl)
│   ├── main.py              # точка входа, lifespan, фоновая чистка, /api/v1/health
│   ├── links.py             # роутер: create(TTL) / redirect(410) / stats / delete / cleanup
│   ├── models.py            # pydantic-схемы (ttl_seconds, expires_at в ответах)
│   ├── database.py          # обёртка над SQLite (aiosqlite), схема с expires_at
│   ├── cache.py             # Redis-кэш (redis-py)
│   ├── geo.py               # определение страны по IP (ip-api.com)
│   ├── shortcuts.py         # генерация short_code (base62, 7 символов)
│   └── config.py            # настройки из переменных окружения
├── static/                  # фронтенд (раздаётся nginx'ом напрямую)
│   ├── index.html
│   ├── css/style.css        # тёплый жёлтый минимализм
│   └── js/app.js            # TTL-чипы, тосты, пасхалки
├── nginx/
│   ├── nginx.conf           # проксирование API и коротких ссылок, статика
│   └── Dockerfile
├── docs/
│   └── ARCHITECTURE.md      # архитектурные решения
├── Dockerfile               # образ приложения (python:3.12-alpine)
├── docker-compose.yml       # app + redis + nginx, healthchecks, тома, TTL-переменные
├── requirements.txt
└── .env.example             # шаблон переменных окружения
```

## Переменные окружения

Значения по умолчанию заданы прямо в `docker-compose.yml`; при необходимости скопируйте `.env.example` в `.env` и переопределите.

| Переменная | По умолчанию | Описание |
|---|---|---|
| `DATABASE_PATH` | `/data/urlshortener.db` | Путь к файлу SQLite на docker-томе |
| `REDIS_URL` | `redis://redis:6379/0` | Подключение к Redis |
| `GEO_API_URL` | `http://ip-api.com/json/{ip}?fields=status,countryCode` | Шаблон гео-запроса (`{ip}` подставляется автоматически) |
| `GEO_TIMEOUT_SECONDS` | `2` | Таймаут внешнего гео-запроса, сек |
| `CACHE_TTL_SECONDS` | `3600` | TTL кэша редиректов `url:{code}`, сек (живёт не дольше ссылки) |
| `GEO_CACHE_TTL_SECONDS` | `86400` | TTL кэша геолокации `geo:{ip}`, сек (24 часа) |
| `BASE_URL` | `http://localhost` | База для сборки `short_url` в ответах API |
| `DEFAULT_TTL_SECONDS` | `3600` | Срок жизни ссылки, если `ttl_seconds` не передан (1 час) |
| `MIN_TTL_SECONDS` | `60` | Минимальный TTL, сек |
| `MAX_TTL_SECONDS` | `604800` | Максимальный TTL, сек (7 дней) |
| `CLEANUP_INTERVAL_SECONDS` | `300` | Период фоновой чистки просроченных ссылок, сек |

## Что изменилось относительно базовой версии

| Область | Было | Стало |
|---|---|---|
| Схема `links` | без срока | колонка `expires_at` + индекс |
| `POST /api/v1/links` | только URL | URL + опциональный `ttl_seconds` |
| Редирект | всегда 302 | 302, но просрочено → **410 Gone** |
| Кэш Redis | фиксированный TTL | `min(cache_ttl, до истечения ссылки)` |
| Фон | только запись аналитики | + цикл чистки просроченных каждые 5 мин |
| Фронтенд | стиль 2000-х | тёплый жёлтый минимализм, чипы TTL |
| API-ответы | без срока | `expires_at` в `LinkResponse` и `StatsResponse` |

## Варианты заданий

Распределение вариантов по участникам (реализуется вариант №3 — эта ветка):

2. Сервис сокращения URL с генерацией QR-кодов — Бондаренко
3. **Временные короткие ссылки — Лукина ✅ (ветка `temp-links`)**
5. Сервис сокращения URL с защитой паролем — Казаченко
9. Сервис с ограничением количества переходов — Гаранин
10. Сервис с монетизацией (реклама) — Кочетков
