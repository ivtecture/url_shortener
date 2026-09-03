# Архитектура сервиса URL Shortener

> Документ проектирования. Стек зафиксирован: Python/FastAPI, SQLite, Redis, nginx, статический фронтенд без сборки, всё в Docker на Alpine.
> Фичи других участников (QR-коды, временные ссылки, пароли, лимиты переходов, монетизация) **не затрагиваются** — поле `expires_at` в схему не входит.

---

## 1. Обзор системы и компонентная диаграмма

Сервис — анонимный сокращатель URL с аналитикой переходов (количество + геолокация по IP). Единственная точка входа — nginx на порту 80: он раздаёт статику фронтенда напрямую с диска и проксирует `/api/v1/*` и короткие ссылки на FastAPI-приложение. Приложение хранит ссылки в SQLite (файл на docker-томе), кэширует маппинг `short_code → original_url` и результаты геолокации в Redis. AMQP-очереди не используются.

```
                       ┌──────────────────────────────────────────────────┐
                       │  docker-compose (сеть: urlshortener_net)         │
                       │                                                  │
  Пользователь         │  ┌───────────────┐      ┌────────────────────┐  │
 ─────────────────────>│  │  nginx :80    │      │  redis :6379       │  │
  GET  /{code}         │  │  (alpine)     │      │  (redis:7-alpine)  │  │
  GET  /static/*       │  └──────┬────────┘      └─────────▲──────────┘  │
  POST /api/v1/links   │         │                         │             │
                       │   статика: /usr/share/nginx/html │ кэш:         │
                       │         │ proxy_pass             │  url:{code}  │
                       │         v                        │  geo:{ip}    │
                       │  ┌────────────────────────────────┴──────────┐  │
                       │  │  app :8000  (FastAPI, python:3.12-alpine)│  │
                       │  │  - REST API /api/v1                      │  │
                       │  │  - редирект GET /{short_code}            │  │
                       │  │  - геолокация: ip-api.com (внешн. API)  ) │  │
                       │  └──────────────┬────────────────────────────┘  │
                       │                 │ aiosqlite (WAL)                │
                       │                 v                                │
                       │  том sqlite-data: /data/urlshortener.db         │
                       │  том redis-data:  /data (redis AOF)             │
                       └──────────────────────────────────────────────────┘

  Внешний вызов геолокации: app ──HTTP──> http://ip-api.com/json/{ip}
  (только для публичных IP, результат кэшируется в Redis, при сбое — 'unknown')
```

**Потоки данных:**

- **Создание ссылки**: браузер → nginx → `POST /api/v1/links` → app → запись в SQLite → ответ с `short_code`.
- **Редирект**: браузер → nginx → app → (кэш Redis) → SQLite → `302 Found` + фоновая запись в `analytics` (IP из `X-Forwarded-For`, страна из кэша/гео-API). Подробно в разделе 6.
- **Статистика**: браузер (модалка) → nginx → app → агрегирующий `SELECT` по `analytics`.
- **Статика**: nginx отдаёт `index.html`, CSS, JS сам, не нагружая Python-воркеры. FastAPI StaticFiles не используется (нет смысла дублировать).

---

## 2. Структура репозитория

```
url_shorter/
├── Readme.md                  # исходные требования
├── docs/
│   └── ARCHITECTURE.md        # этот документ
├── app/                       # бэкенд-пакет
│   ├── __init__.py
│   ├── main.py                # создание FastAPI, регистрация роутеров, healthcheck
│   ├── config.py              # настройки из переменных окружения (pydantic-settings)
│   ├── database.py            # подключение aiosqlite, PRAGMA (WAL, busy_timeout, FK)
│   ├── models.py              # pydantic-схемы запросов/ответов
│   ├── links.py               # роутер: POST /api/v1/links, GET /{short_code},
│   │                          #         GET /stats, DELETE /delete/{short_code}
│   ├── shortcuts.py           # генерация short_code (base62, коллизии)
│   ├── geo.py                 # геолокация IP: ip-api.com + кэш Redis + fallback
│   └── cache.py               # обёртка над redis.asyncio
├── static/                    # фронтенд без сборки — раздаётся nginx'ом
│   ├── index.html             # главная страница с формой + мем
│   ├── css/
│   │   └── style.css          # стиль 2000-х, холодные тона, анимации input'ов
│   └── js/
│       └── app.js             # fetch-вызовы API, модалка статистики, копирование
├── nginx/
│   ├── nginx.conf             # конфиг: статики + proxy_pass + заголовки X-Forwarded-*
│   └── Dockerfile             # FROM nginx:1.27-alpine + копия конфига
├── Dockerfile                 # app: FROM python:3.12-alpine
├── docker-compose.yml         # сервисы nginx, app, redis; томы; healthchecks
├── requirements.txt           # fastapi, uvicorn, aiosqlite, redis, httpx, pydantic-settings
├── .env.example               # шаблон переменных окружения
└── .gitignore                 # *.db, .env, __pycache__
```

---

## 3. Схема БД SQLite

Адаптация схемы из Readme: `BIGSERIAL` → `INTEGER PRIMARY KEY AUTOINCREMENT`, `INET` → `TEXT`, `TIMESTAMP` → `TEXT` (ISO 8601 UTC), поле `expires_at` **убрано** (фича другого участника). Даты храним в UTC через `datetime('now')` (гринвич), чтобы не зависеть от TZ контейнера.

```sql
-- Таблица ссылок
CREATE TABLE IF NOT EXISTS links (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    short_code   TEXT    NOT NULL UNIQUE,              -- 7 символов base62
    original_url TEXT    NOT NULL,                     -- http/https, максимум 2048
    created_at   TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- unique-ограничение уже создаёт индекс; отдельный CREATE INDEX не нужен,
-- но оставляем явное имя для читаемости миграций:
CREATE UNIQUE INDEX IF NOT EXISTS idx_links_short_code ON links(short_code);

-- Таблица аналитики: одна строка = один переход
CREATE TABLE IF NOT EXISTS analytics (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    link_id    INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    clicked_at TEXT    NOT NULL DEFAULT (datetime('now')),
    ip_address TEXT,                                    -- текстовое представление IPv4/IPv6
    country    TEXT                                     -- ISO 3166-1 alpha-2 | 'local' | 'unknown'
);

CREATE INDEX IF NOT EXISTS idx_analytics_link_id ON analytics(link_id);
-- составный индекс под агрегацию статистики (link_id + срез по времени)
CREATE INDEX IF NOT EXISTS idx_analytics_link_clicked ON analytics(link_id, clicked_at);
```

**Обязательные PRAGMA при каждом подключении** (выполняются в `app/database.py`):

```sql
PRAGMA journal_mode = WAL;      -- читатели не блокируют писателя
PRAGMA busy_timeout = 5000;     -- ждать блокировку до 5 сек вместо мгновенной ошибки
PRAGMA foreign_keys = ON;       -- каскадное удаление analytics при удалении links
PRAGMA synchronous = NORMAL;    -- разумный баланс скорость/надёжность для WAL
```

`ON DELETE CASCADE` обеспечивает: при `DELETE` из `links` строка `analytics` уходит автоматически — отдельного запроса на очистку не нужно.

---

## 4. Спецификация API

База: `http://localhost` (через nginx). Формат ошибок — стандартный для FastAPI:

```json
{ "detail": "текст ошибки" }
```

### 4.1. POST /api/v1/links — создать короткую ссылку

**Запрос** (`Content-Type: application/json`):

```json
{ "original_url": "https://example.com/some/very/long/path?query=42&utm_source=mail" }
```

Правила валидации (pydantic):
- обязательное поле `original_url`, строка;
- валидный URL со схемой `http` или `https` (бизнес-правила: `ftp://`, `javascript:` и прочее — `400`);
- длина ≤ 2048 символов;
- пустое тело / не-JSON → `422` от pydantic автоматически.

**Успех — 201 Created:**

```json
{
  "short_code": "aB3xK9z",
  "short_url": "http://localhost/aB3xK9z",
  "original_url": "https://example.com/some/very/long/path?query=42&utm_source=mail",
  "created_at": "2026-09-03 08:00:00"
}
```

Повторное создание того же URL создаёт **новую** запись с новым `short_code` (сервис анонимный, дедупликация не выполняется — осознанное упрощение, см. риски).

**Ошибки:**

| Код | Случай | Пример тела |
|-----|--------|-------------|
| 400 | схема не http/https, длина > 2048 | `{"detail": "Поддерживаются только схемы http и https"}` |
| 422 | невалидный JSON, пустое поле, не-URL | `{"detail": [{"loc": ["body", "original_url"], "msg": "Input should be a valid URL", ...}]}` |
| 503 | SQLite/Redis недоступны | `{"detail": "База данных недоступна"}` |

### 4.2. GET /{short_code} — редирект

`short_code` — ровно 7 символов base62; для всего, что длиннее/короче или не входит в алфавит, можно сразу отдавать `404` без обращения к БД.

**Успех — 302 Found** (не 301: браузеры кэшируют `301` навсегда и перестают ходить на сервер, что убивает аналитику):

```
HTTP/1.1 302 Found
Location: https://example.com/some/very/long/path?query=42&utm_source=mail
Cache-Control: no-store
```

**Ошибки:**

| Код | Случай | Тело |
|-----|--------|------|
| 404 | код не найден или ссылка удалена | `{"detail": "Ссылка не найдена"}` |

### 4.3. GET /api/v1/links/{short_code}/stats — статистика

**Успех — 200 OK:**

```json
{
  "short_code": "aB3xK9z",
  "original_url": "https://example.com/some/very/long/path?query=42&utm_source=mail",
  "created_at": "2026-09-03 08:00:00",
  "clicks": 128,
  "countries": [
    { "country": "RU", "count": 71 },
    { "country": "KZ", "count": 30 },
    { "country": "local", "count": 21 },
    { "country": "unknown", "count": 6 }
  ]
}
```

Агрегирующий запрос:

```sql
SELECT a.country, COUNT(*) AS cnt
FROM analytics a
WHERE a.link_id = ?
GROUP BY a.country
ORDER BY cnt DESC;
```

**Ошибки:** `404` — код не найден (`{"detail": "Ссылка не найдена"}`).

### 4.4. DELETE /api/v1/links/delete/{short_code} — удалить ссылку

Физическое удаление строки из `links` (аналитика уходит каскадом). Кэш `url:{short_code}` инвалидируется (`DEL`) сразу после удаления.

**Успех — 200 OK:**

```json
{ "short_code": "aB3xK9z", "deleted": true }
```

**Ошибки:**

| Код | Случай | Тело |
|-----|--------|------|
| 404 | код не найден | `{"detail": "Ссылка не найдена"}` |

Сервис анонимный → владение не проверяется: удалить может любой, кто знает `short_code` (компромисс, см. раздел 9).

### 4.5. GET /api/v1/health — служебный

`200 {"status": "ok"}` — используется healthcheck'ами Docker и nginx (`location /api/v1/health`). В ответе также флаги `db: true/false`, `redis: true/false` по факту доступности зависимостей.

---

## 5. Алгоритм генерации short_code

- **Длина**: 7 символов.
- **Алфавит base62**: `0-9` (10) + `a-z` (26) + `A-Z` (26) = 62 символа.
- **Пространство**: 62⁷ ≈ 3.5 × 10¹² комбинаций — перебор URL-ов посторонним практически исключён даже при миллиардах записей.
- **Способ**: криптографически стойкий генератор (`secrets.choice` из стандартной библиотеки — модуль `app/shortcuts.py`), а не инкрементальный счётчик. Причина: непредсказуемость (нельзя угадать соседние ссылки перебором id), удаление ссылок не создаёт «дырок».

```python
ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

def generate_short_code(length: int = 7) -> str:
    return "".join(secrets.choice(ALPHABET) for _ in range(length))
```

**Проверка коллизий** — двухступенчатая:

1. `INSERT INTO links (short_code, original_url) VALUES (?, ?)` — уникальный индекс сам отклонит дубликат.
2. При `sqlite3.IntegrityError` (совпадение с существующим кодом) — перегенерация и повторная вставка, максимум **5 попыток**, затем `503`. Вероятность коллизии при 10 млн активных ссылок ≈ 10⁷ / 3.5×10¹² ≈ 0.0003% на попытку, поэтому на практике вторая попытка не требуется.

Атомарности «проверил → вставил» не требуется: UNIQUE-индекс в SQLite — сам гарант целостности, `SELECT` перед вставкой был бы гонкой.

---

## 6. Поток редиректа (главный сценарий)

```
1. Браузер ── GET /aB3xK9z ─────────────────────> nginx :80
2. nginx: это не /static/* и не /api/v1/* ──> proxy_pass http://app:8000
   (nginx добавляет: X-Real-IP и X-Forwarded-For с IP клиента)
3. app: GET url:aB3xK9z из Redis
   ├── HIT  → original_url из кэша
   └── MISS → SELECT original_url FROM links WHERE short_code = ?
              └─ найдено → SETEX url:aB3xK9z 3600 <original_url>
              └─ не найдено → 404 {"detail": "Ссылка не найдена"} (конец)
4. app: FastAPI BackgroundTasks — фоновая запись перехода (не тормозит ответ):
   a) IP клиента = первый адрес из заголовка X-Forwarded-For
      (за nginx подделать заголовок нельзя: nginx перезаписывает X-Real-IP;
      в настройках proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for)
   b) геолокация:
      - IP приватный (10/8, 172.16/12, 192.168/16, 127.*, ::1, fc00::/7)
        → country = 'local' (без внешних запросов)
      - иначе: GET geo:{ip} из Redis
        ├── HIT  → страна из кэша (TTL 24 часа)
        └── MISS → HTTP GET http://ip-api.com/json/{ip}?fields=status,countryCode
                    ├─ 200 {"status":"success","countryCode":"RU"} → 'RU'
                    │     → SETEX geo:{ip} 86400 'RU'
                    └─ таймаут 2 сек / ошибка / лимит → 'unknown'
                          (переход фиксируется, страна уточнится при следующих
                           переходах с этого же IP — кэш не отравляем)
   c) INSERT INTO analytics (link_id, ip_address, country) VALUES (?, ?, ?)
5. app ── 302 Found, Location: original_url ──> nginx ──> браузер
```

**Ключевые детали:**

- **Кэш редиректа**: `url:{short_code}` → `original_url`, TTL 3600 с. Удаление ссылки делает `DEL` ключа — после этого кэш не «воскрешает» удалённую ссылку.
- **Кэш гео**: `geo:{ip}` → ISO-код страны, TTL 86400 с (24 ч). Снижает число обращений к ip-api.com (бесплатный лимит — 45 запросов/мин с одного IP сервера) до ~«число уникальных IP в сутки».
- **Честность данных**: запись в `analytics` идёт после отправки ответа (BackgroundTasks), потеря перехода при падении контейнера допустима — это осознанный компромисс скорости и точности.
- **Неблокирующий ответ**: даже при полностью лежащем гео-API редирект не задержится дольше 2 сек (таймаут httpx), страна будет `unknown`.

---

## 7. Конфигурация Docker

### 7.1. docker-compose.yml (проектная)

Три сервиса, всё на Alpine. Приложение **не публикует** портов наружу — только nginx :80 (единственный entrypoint). Redis тоже закрыт от хоста.

```yaml
services:
  app:
    build: .                                    # Dockerfile: python:3.12-alpine
    container_name: urlshortener-app
    restart: unless-stopped
    environment:
      DATABASE_PATH: /data/urlshortener.db
      REDIS_URL: redis://redis:6379/0
      GEO_API_URL: http://ip-api.com/json/{ip}?fields=status,countryCode
      CACHE_TTL_SECONDS: "3600"
      GEO_CACHE_TTL_SECONDS: "86400"
      GEO_TIMEOUT_SECONDS: "2"
      BASE_URL: http://localhost                 # для сборки short_url в ответах
    volumes:
      - sqlite-data:/data
    depends_on:
      redis:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8000/api/v1/health"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s

  redis:
    image: redis:7-alpine
    container_name: urlshortener-redis
    restart: unless-stopped
    command: redis-server --appendonly yes --maxmemory 128mb --maxmemory-policy allkeys-lru
    volumes:
      - redis-data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 10s
      timeout: 3s
      retries: 3

  nginx:
    build: ./nginx                              # FROM nginx:1.27-alpine + nginx.conf
    container_name: urlshortener-nginx
    restart: unless-stopped
    ports:
      - "80:80"                                 # единственный вход снаружи
    volumes:
      - ./static:/usr/share/nginx/html:ro       # статику раздаёт сам nginx
    depends_on:
      app:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/api/v1/health"]
      interval: 30s
      timeout: 5s
      retries: 3

volumes:
  sqlite-data:                                  # файл БД переживает пересборку
  redis-data:                                   # AOF-персистентность кэша (тёплый старт)
```

### 7.2. nginx.conf — суть конфигурации

```nginx
server {
    listen 80;

    # 1. Статика фронтенда — с диска, без участия Python
    location / {
        root /usr/share/nginx/html;
        index index.html;
        try_files $uri $uri/ /index.html;       # SPA-фолбэк не критичен, но безвреден
    }

    # 2. API — на FastAPI
    location /api/v1/ {
        proxy_pass http://app:8000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }

    # 3. Короткие ссылки (7 символов base62) — на FastAPI, до try_files
    location ~ ^/[0-9a-zA-Z]{7}$ {
        proxy_pass http://app:8000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

Регулярка `^/[0-9a-zA-Z]{7}$` в nginx отсекает заведомо невалидные коды ещё до Python; настоящие файлы статики (например `/css/style.css`) под локацию не попадают — она перекрывается `location /` только для несовпавших URI (nginx выбирает точное совпадение regex первым при таком порядке).

### 7.3. Переменные окружения (`.env.example`)

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `DATABASE_PATH` | `/data/urlshortener.db` | путь к файлу SQLite на томе |
| `REDIS_URL` | `redis://redis:6379/0` | подключение к Redis |
| `GEO_API_URL` | `http://ip-api.com/json/{ip}?fields=status,countryCode` | шаблон гео-запроса |
| `GEO_TIMEOUT_SECONDS` | `2` | таймаут внешнего гео-запроса |
| `CACHE_TTL_SECONDS` | `3600` | TTL кэша редиректов |
| `GEO_CACHE_TTL_SECONDS` | `86400` | TTL кэша гео (24 ч) |
| `BASE_URL` | `http://localhost` | база для `short_url` в ответах API |

---

## 8. Фронтенд

Статические файлы без сборки, отдаёт nginx. Один экран + одна модалка.

### 8.1. Главная страница (`static/index.html`)

- **Форма**: поле «Вставьте длинный URL» + кнопка «Сократить!».
- **Результат**: короткий URL, кнопка «Копировать» (Clipboard API), кнопка «Статистика» (открывает модалку), кнопка «Удалить».
- **Мем про программирование**: картинка/подпись в духе классики 2000-х — например, «It works on my machine» с печатающимся эффектом; размещается в «сайдбаре»-таблице как визитка эпохи.
- **Стиль 2000-х, приглушённые холодные тона**: палитра `#2f4f4f` (dark slate), `#4682b4` (steel blue), `#b0c4de` (light steel), фон с лёгким градиентом; bevel-рамки у кнопок (border outset/inset), моноширинный системный шрифт для кодов, «стеклянные» полосатые заголовки таблиц как у старых форумов.
- **Выразительная анимация полей ввода**: при фокусе — рамка «загорается» холодным свечением (box-shadow + transition), мигающий caret-стилизация, плавное «всплытие» label'а над полем, placeholder «печатается» посимвольно.

### 8.2. Модалка статистики

- Открывается кнопкой «Статистика» у созданной ссылки (и по введённому коду).
- Содержимое: короткий URL, исходный URL, дата создания, крупный счётчик переходов, таблица «Страна — Переходов» (сортировка по убыванию), кнопка «Обновить» (повторный fetch).
- Закрытие: крестик, клик по фону, Esc.

### 8.3. JS-код вызова API (`static/js/app.js`)

Ванильный JS, без зависимостей:

```js
// 1. Создание ссылки
const res = await fetch('/api/v1/links', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ original_url: input.value })
});
if (res.status === 201) {
  const data = await res.json();          // { short_code, short_url, ... }
  showResult(data);
} else {
  const err = await res.json();           // { detail: ... } — 400/422
  showError(err.detail);
}

// 2. Статистика (в модалке)
const stats = await fetch(`/api/v1/links/${shortCode}/stats`);
if (stats.ok) renderStats(await stats.json());   // { clicks, countries: [...] }

// 3. Удаление
const del = await fetch(`/api/v1/links/delete/${shortCode}`, { method: 'DELETE' });

// 4. Копирование в буфер
await navigator.clipboard.writeText(shortUrl);
```

Роутинг на стороне сервера для UI не нужен: `GET /` отдаёт `index.html` из статики nginx, короткая ссылка `/aB3xK9z` перехватывается regex-локацией до статики и уходит в редирект.

---

## 9. Риски и компромиссы

| # | Риск / компромисс | Митигация / принятое решение |
|---|---|---|
| 1 | **SQLite под параллельной записью** — один писатель одновременно; вставки analytics при всплеске переходов могут конфликтовать | `PRAGMA journal_mode=WAL` (читатели не блокируют писателя), `busy_timeout=5000` (мягкое ожидание блокировки), `synchronous=NORMAL`. Запись переходов вынесена в BackgroundTasks после ответа клиенту. При типичной нагрузке пет-проекта (десятки переходов/сек) узкого места нет; вертикальный рост — переход на PostgreSQL. |
| 2 | **Честность аналитики без регистрации**: статистика и удаление доступны любому, знающему `short_code`; повторы одного пользователя считаются; боты не фильтруются | Осознанный компромисс анонимного сервиса. Пространство 62⁷ кодов делает угадывание чужих ссылок практически невозможным. Базовая гигиена: игнорировать заголовок `User-Agent` популярных краулеров при записи analytics (опционально, документируется как ограничение). |
| 3 | **DELETE без владения** — любой может удалить ссылку, узнав код | Принято в рамках ТЗ (сервис анонимный). Смягчение: физическое удаление + каскад + инвалидация кэша; перебор 62⁷ вариантов экономически бессмыслен. |
| 4 | **Внешнее гео-API (ip-api.com)**: лимит 45 запросов/мин, недоступность, бесплатный тариф только по HTTP (без TLS) | Кэш `geo:{ip}` в Redis на 24 ч радикально снижает число вызовов (один вызов на уникальный IP в сутки). Таймаут 2 с, при сбое — страна `unknown`, переход фиксируется всегда. Приватные IP — `local` без внешних вызовов. Через HTTP уходит только IP-адрес, никаких URL пользователей. Fallback-цепочка: Redis → ip-api.com → `'unknown'`; при необходимости расширения — заменяемый `GEO_API_URL`. |
| 5 | **302 вместо 301** — каждый переход бьёт по серверу | Выбрано сознательно: `301` кэшируется браузером навсегда и аналитика умирает. `Cache-Control: no-store` на редиректе. |
| 6 | **Нет дедупликации URL** — один и тот же длинный URL можно сокращать многократно | Упрощение: без пользователей некому «владеть» ссылкой; консолидация сломала бы раздельную статистику. |
| 7 | **Потеря последних переходов при падении контейнера** (BackgroundTasks после ответа) | Приемлемо для аналитического счётчика. Полная надёжность потребовала бы очереди — исключена по стеку (без AMQP). |
| 8 | **Redis as cache, не source of truth** — потеря кэша = промахи в SQLite | `maxmemory-policy allkeys-lru`, AOF-персистентность на томе `redis-data` для тёплого старта. Деградация производительная, не функциональная. |

---

## Приложение: сверка с требованиями Readme

| Требование | Где реализовано |
|---|---|
| Создание короткой ссылки | `POST /api/v1/links` (раздел 4.1) |
| Редирект | `GET /{short_code}` (разделы 4.2, 6) |
| Аналитика: переходы, геолокация | `GET /api/v1/links/{short_code}/stats` (раздел 4.3), поток 6 |
| Удаление | `DELETE /api/v1/links/delete/{short_code}` (раздел 4.4) |
| Без регистрации | анонимные эндпоинты, компромиссы 2–3 |
| UI 2000-х, модалка, мем, холодные тона, анимация | раздел 8 |
| Alpine, Redis, SQLite, Python, reverse proxy | разделы 1, 7 |
| AMQP не нужен | нагрузка не требует; асинхронность — FastAPI + BackgroundTasks |
