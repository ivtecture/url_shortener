# Архитектура сервиса URL Shortener

> Документ проектирования. Активный стек: **Go 1.27** (`app/v2`), SQLite, Redis, nginx, статический фронтенд без сборки, всё в Docker на Alpine. Реализация на Python/FastAPI (`app/`) сохранена как legacy и трафика не получает.
> Вариант задания №10 (монетизация рекламой) реализован: страница перехода показывает баннер, показы и клики учитываются. Фичи остальных участников (QR-коды, временные ссылки, пароли, лимиты переходов) **не затрагиваются** — поле `expires_at` в схему не входит.

---

## 1. Обзор системы и компонентная диаграмма

Сервис — анонимный сокращатель URL с аналитикой переходов (количество + геолокация по IP) и показом рекламы на странице перехода. Единственная точка входа — nginx на порту 80: он раздаёт статику фронтенда напрямую с диска и проксирует `/api/v2/*`, `/ad-click/*` и короткие ссылки на Go-приложение `v2`. Приложение хранит ссылки и агрегаты рекламы в SQLite (файл на docker-томе), кэширует маппинг `short_code → original_url` и результаты геолокации в Redis. AMQP-очереди не используются.

```
                       ┌──────────────────────────────────────────────────────┐
                       │  docker-compose (сеть: urlshortener_net)             │
                       │                                                      │
   Пользователь        │  ┌───────────────┐      ┌────────────────────┐      │
  ─────────────────────>│  │  nginx :80    │      │  redis :6379       │      │
   GET  /{code}         │  │  (alpine)     │      │  (redis:7-alpine)  │      │
   GET  /static/*       │  └──────┬────────┘      └─────────▲──────────┘      │
   GET  /ad-click/{key} │         │                         │                 │
   POST /api/v2/links   │   статика: /usr/share/nginx/html │ кэш:             │
                       │         │ proxy_pass             │  url:{code}      │
                       │         v                        │  geo:{ip}        │
                       │  ┌────────────────────────────────┴──────────┐      │
                       │  │  v2 :8000  (Go 1.27, golang:1.27-alpine)  │      │
                       │  │  - REST API /api/v2                          │      │
                       │  │  - страница перехода GET /{short_code}      │      │
                       │  │  - клик по баннеру GET /ad-click/{ad_key}   │      │
                       │  │  - геолокация: ip-api.com (внешн. API)     │      │
                       │  └──────────────┬────────────────────────────┘      │
                       │                 │ modernc.org/sqlite (WAL)             │
                       │                 v                                    │
                       │  том sqlite-data: /data/urlshortener.db             │
                       │  том redis-data:  /data (redis AOF)                 │
                       └──────────────────────────────────────────────────────┘

   Внешний вызов геолокации: v2 ──HTTP──> http://ip-api.com/json/{ip}
   (только для публичных IP, результат кэшируется в Redis, при сбое — 'unknown')

   app :8000 (FastAPI, legacy) — тоже смонтирован на том sqlite-data,
   но nginx проксирует на него только /api/v1-legacy/* для сверки.
```

**Потоки данных:**

- **Создание ссылки**: браузер → nginx → `POST /api/v2/links` → v2 → запись в SQLite → ответ с `short_code`.
- **Переход по короткой ссылке**: браузер → nginx → v2 → (кэш Redis) → SQLite → `200 OK` + HTML-страница с рекламой и таймером 5 с; по кнопке «Продолжить» браузер уходит на оригинальный URL. Для клиентов без `Accept: text/html` — сразу `302 Found`. Подробно в разделе 6.
- **Клик по баннеру**: браузер → nginx `/ad-click/{ad_key}` → v2 → учёт клика → `302 Found` на сайт рекламодателя.
- **Статистика**: браузер (модалка) → nginx → v2 → агрегирующий `SELECT` по `analytics`; агрегаты рекламы — по `ad_stats`.
- **Статика**: nginx отдаёт `index.html`, CSS, JS и картинки баннеров сам, не нагружая приложение.

---

## 2. Структура репозитория

```
url_shortener/
├── Readme.md                  # исходные требования
├── docs/
│   └── ARCHITECTURE.md        # этот документ
├── app/
│   ├── v2/                    # АКТИВНЫЙ бэкенд (Go)
│   │   ├── main.go            # конфиг, инициализация, graceful shutdown
│   │   ├── api.go             # роутер и обработчики HTTP
│   │   ├── ads.go             # баннеры: разбор файлов, веса, учёт показов/кликов
│   │   ├── db.go              # схема SQLite, PRAGMA, обёртка над БД
│   │   ├── cache.go           # обёртка над go-redis
│   │   ├── geo.go             # геолокация IP: ip-api.com + кэш Redis + fallback
│   │   ├── routing_page.html  # шаблон страницы перехода с рекламой
│   │   ├── shortcuts/         # генерация short_code (base58, коллизии)
│   │   ├── go.mod / go.sum
│   │   └── Dockerfile         # multi-stage: CGO_ENABLED=0, non-root
│   ├── __init__.py            # LEGACY-бэкенд (Python/FastAPI)
│   ├── main.py                # создание FastAPI, регистрация роутеров, healthcheck
│   ├── config.py              # настройки из переменных окружения (pydantic-settings)
│   ├── database.py            # подключение aiosqlite, PRAGMA (WAL, busy_timeout, FK)
│   ├── models.py              # pydantic-схемы запросов/ответов
│   ├── links.py               # роутер v1: POST /api/v1/links, GET /{short_code}, ...
│   ├── shortcuts.py           # генерация short_code v1
│   ├── geo.py                 # геолокация IP v1
│   └── cache.py               # обёртка над redis.asyncio
├── static/                    # фронтенд без сборки — раздаётся nginx'ом
│   ├── index.html             # главная страница: форма, модалка статистики, байки
│   ├── css/
│   │   └── style.css          # холодная палитра, фактура эпохи, анимации полей
│   ├── js/
│   │   └── app.js             # fetch-вызовы API, модалка, копирование
│   └── ads/                   # баннеры + ad_images.txt + ad_texts.txt
├── nginx/
│   ├── nginx.conf             # статика + proxy_pass + X-Forwarded-*
│   └── Dockerfile             # FROM nginx:1.27-alpine + копия конфига
├── Dockerfile                 # legacy app: FROM python:3.12-alpine
├── docker-compose.yml         # сервисы v2, app, redis, nginx; томы; healthchecks
├── requirements.txt           # fastapi, uvicorn, aiosqlite, redis, httpx, pydantic-settings
├── .env.example               # шаблон переменных окружения
└── .gitignore                 # *.db, .env, __pycache__
```

---

## 3. Схема БД SQLite

Адаптация схемы из Readme: `BIGSERIAL` → `INTEGER PRIMARY KEY AUTOINCREMENT`, `INET` → `TEXT`, `TIMESTAMP` → `TEXT` (UTC), поле `expires_at` **убрано** (фича другого участника). Даты храним в UTC через `datetime('now')` (гринвич), чтобы не зависеть от TZ контейнера.

Фактический источник схемы — `app/v2/db.go`; ниже он приведён дословно. Python-реализация создаёт совместимые таблицы (`app/database.py`).

```sql
-- Таблица ссылок
CREATE TABLE IF NOT EXISTS links (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    short_code   TEXT    NOT NULL UNIQUE,              -- 7 символов base58
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
-- составной индекс под агрегацию статистики (link_id + срез по времени)
CREATE INDEX IF NOT EXISTS idx_analytics_link_clicked ON analytics(link_id, clicked_at);

-- Накопленные счётчики рекламы (вариант задания №10).
-- Одна строка = агрегат (баннер × тип события × ссылка), а не событие лог:
-- пишем пачками раз в AD_FLUSH_INTERVAL_SECONDS, поэтому таблица остаётся
-- компактной. link_id = 0 — сквозной итог по баннеру, link_id > 0 — по ссылке.
CREATE TABLE IF NOT EXISTS ad_stats (
    ad_key     TEXT    NOT NULL,                        -- имя файла баннера
    event_type TEXT    NOT NULL,                        -- impression | click
    link_id    INTEGER NOT NULL,
    count      INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT    NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (ad_key, event_type, link_id)
);

CREATE INDEX IF NOT EXISTS idx_ad_stats_link ON ad_stats(link_id);
```

**Обязательные PRAGMA при каждом подключении** (выполняются в `app/v2/db.go`, аналогично в `app/database.py`):

```sql
PRAGMA journal_mode = WAL;      -- читатели не блокируют писателя
PRAGMA busy_timeout = 5000;     -- ждать блокировку до 5 сек вместо мгновенной ошибки
PRAGMA foreign_keys = ON;       -- каскадное удаление analytics при удалении links
PRAGMA synchronous = NORMAL;    -- разумный баланс скорость/надёжность для WAL
```

`ON DELETE CASCADE` обеспечивает: при `DELETE` из `links` строка `analytics` уходит автоматически — отдельного запроса на очистку не нужно.

Счётчики `ad_stats` каскадом **не** удаляются: аналитика рекламы по баннеру (`link_id = 0`) должна пережить удаление ссылки, а строки с `link_id > 0` осиротеют. Это осознанно — объём мусора ограничен числом удалённых ссылок, а цена очистки — лишние записи в общий файл SQLite.

---

## 4. Спецификация API

База: `http://localhost` (через nginx). Описан **активный (v2) API**. Legacy-пути `/api/v1-legacy/*` обслуживает Python-реализация с прежней семантикой редиректа (`302`), в разделах 4.1–4.5 она не описана.

Формат ошибок — простой JSON:

```json
{ "detail": "текст ошибки" }
```

### 4.1. POST /api/v2/links — создать короткую ссылку

**Запрос** (`Content-Type: application/json`):

```json
{ "original_url": "https://example.com/some/very/long/path?query=42&utm_source=mail" }
```

Правила валидации (`app/v2/api.go`):
- обязательное поле `original_url`, строка;
- валидный URL со схемой `http` или `https` (бизнес-правила: `ftp://`, `javascript:` и прочее — `400`);
- длина ≤ 2048 символов;
- пустое тело / не-JSON → `422`.

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
| 422 | невалидный JSON, пустое поле, не-URL | `{"detail": "Input should be a valid URL"}` |
| 503 | SQLite недоступен или код не сгенерировался за 5 попыток | `{"detail": "База данных недоступна"}` |

### 4.2. GET /{short_code} — страница перехода с рекламой

`short_code` — ровно 7 символов алфавита base58; для всего, что длиннее/короче или не входит в алфавит, сразу `404` без обращения к БД.

**Семантика изменилась:** раньше ответ был `302 Found` + `Location`, теперь браузер получает **`200 OK` + HTML-страницу** с рекламным баннером, кольцевым таймером на 5 секунд и кнопкой «Продолжить», которая выполняет переход на оригинальный URL. Промежуточная страница нужна для показа рекламы (вариант задания №10) — в `302` баннер показать негде.

Заголовки ответа:

```
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
Cache-Control: no-store
Referrer-Policy: no-referrer
X-Robots-Tag: noindex, nofollow
```

Тело — результат выполнения шаблона `app/v2/routing_page.html` с подстановками `TargetURL`, `AdImage`, `AdLink`, `AdText`. Экранирование выполняет `html/template` (важно: `TargetURL` попадает внутрь JS-строки, где контекстная экранизация Go экранирует и кавычки, и слэши).

**Для программных клиентов — обычный `302 Found` без рекламы.** Если в `Accept` нет `text/html` (обычный `curl` шлёт `*/*`), отдаётся редирект без промежуточной страницы:

```
HTTP/1.1 302 Found
Location: https://example.com/some/very/long/path?query=42&utm_source=mail
Cache-Control: no-store
```

Причина: баннер всё равно никому не покажется, а лишняя HTML-страница ломает скрипты, ботов и превью-ссылок в мессенджерах. Правило реализовано в `prefersJSON` (`app/v2/api.go`).

**Ошибки:**

| Код | Случай | Тело |
|-----|--------|------|
| 404 | код не найден или ссылка удалена | `{"detail": "Ссылка не найдена"}` |

### 4.3. GET /ad-click/{ad_key} — клик по баннеру

Служебный счётчик: `302` на рекламодателя после записи клика в буфер метрик. Ключ баннера ограничен регулярным выражением `^[A-Za-z0-9._-]{1,64}$`, а схема цели проверяется по белому списку `http`/`https` — файл `ad_images.txt` лежит в публично отдаваемом `static/ads/`, и без проверки в `Location` мог бы уйти `javascript:` или `data:`. Неизвестный ключ, недопустимая схема и попытка выйти из каталога дают `404`. Клик по баннеру засчитывается и глобально (`link_id = 0`), и для конкретной ссылки из query-параметра `link`.

Служебный эндпоинт рекламы. Баннер на странице перехода ведёт **не** напрямую на сайт рекламодателя, а сюда: только так сервер узнаёт о клике и может посчитать CTR.

- `ad_key` — имя файла баннера из `ad_images.txt`, проверяется регулярным выражением `^[A-Za-z0-9._-]{1,64}$` (заодно не даёт выйти за пределы `static/ads/`).
- Схема цели проверяется по белому списку `http`/`https`: файл `ad_images.txt` лежит в публично отдаваемом `static/ads/`, и без проверки в `Location` мог бы уйти `javascript:` или `data:`.
- Неизвестный ключ или баннер с недопустимой целью → `404`; ключ с попыткой выйти из каталога отсекается регулярным выражением (`404` при обращении к приложению напрямую, `400` от nginx — тот не пропускает `%2f` в URI).

**Успех — 302 Found** на сайт рекламодателя, перед ответом фиксируется событие `click`:

```
HTTP/1.1 302 Found
Location: https://sibsiu.ru
Cache-Control: no-store
Referrer-Policy: no-referrer
```

### 4.4. GET /api/v2/ads/stats — агрегаты по рекламе

**Успех — 200 OK:**

```json
{
  "ads": [
    { "ad_key": "altman.jpg", "impressions": 120, "clicks": 9, "ctr": 0.075 },
    { "ad_key": "cpp.jpg",    "impressions": 80,  "clicks": 2, "ctr": 0.025 }
  ],
  "total_impressions": 200,
  "total_clicks": 11,
  "ctr": 0.055
}
```

CTR считается как `clicks / impressions`, при нулевых показах — `0`. Перед чтением буфер метрик принудительно сбрасывается в БД, иначе ответ отставал бы от реальности на `AD_FLUSH_INTERVAL_SECONDS`.

### 4.5. GET /api/v2/links/{short_code}/ads — реклама по конкретной ссылке

**Успех — 200 OK:**

```json
{ "short_code": "aB3xK9z", "impressions": 14, "clicks": 1, "ctr": 0.0714 }
```

**Ошибки:** `404` — код не найден или код невалиден.

### 4.6. GET /api/v2/links/{short_code}/stats — статистика переходов

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

### 4.7. DELETE /api/v2/links/delete/{short_code} — удалить ссылку

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

Счётчики рекламы по удалённой ссылке в `ad_stats` остаются: каскада там нет намеренно (см. раздел 3).

### 4.8. GET /api/v2/health — служебный

`200 {"status": "ok"}` — используется healthcheck'ами Docker и nginx. В ответе также флаги `db: true/false`, `redis: true/false` по факту доступности зависимостей.

---

## 5. Алгоритм генерации short_code

- **Длина**: 7 символов.
- **Алфавит base58**: из base62 убраны `0`, `O`, `I`, `l`, чтобы исключить визуально неоднозначные коды (`0`/`O`, `1`/`l`).
- **Пространство**: 58⁷ ≈ 2.2 × 10¹² комбинаций — перебор URL-ов посторонним практически исключён даже при миллиардах записей.
- **Способ**: криптографически стойкий генератор (`crypto/rand`, модуль `app/v2/shortcuts`), а не инкрементальный счётчик. Причина: непредсказуемость (нельзя угадать соседние ссылки перебором id), удаление ссылок не создаёт «дырок».

```go
const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func generate() string {
    code := make([]byte, length)
    for i := range code {
        // rejection sampling: отбрасываем байты >= 256-(256%len(alphabet)),
        // иначе остаток от деления искажает распределение по алфавиту
        ...
    }
    return string(code)
}
```

**Проверка коллизий** — двухступенчатая:

1. `INSERT INTO links (short_code, original_url) VALUES (?, ?)` — уникальный индекс сам отклонит дубликат.
2. При нарушении уникальности — регенерация и повторная вставка, максимум **5 попыток**, затем `503`. Вероятность коллизии при 10 млн активных ссылок ≈ 10⁷ / 2.2×10¹² ≈ 0.0005% на попытку, поэтому на практике вторая попытка не требуется.

Атомарности «проверил → вставил» не требуется: UNIQUE-индекс в SQLite — сам гарант целостности, `SELECT` перед вставкой был бы гонкой.

---

## 6. Поток перехода по короткой ссылке (главный сценарий)

```
1. Браузер ── GET /aB3xK9z ─────────────────────> nginx :80
2. nginx: путь из 7 символов base58 ──> proxy_pass http://v2:8000
   (regex-локация "^/[0-9a-zA-Z]{7}$" выигрывает у префиксной "/",
    поэтому статика сюда не попадает; nginx добавляет X-Real-IP и
    X-Forwarded-For с IP клиента)
3. v2: GET url:aB3xK9z из Redis
   ├── HIT  → link_id + original_url из кэша (значение "id:url")
   └── MISS → SELECT id, original_url FROM links WHERE short_code = ?
              ├─ найдено → SETEX url:aB3xK9z 3600 "<id>:<original_url>"
              └─ не найдено → 404 {"detail": "Ссылка не найдена"} (конец)
4. v2: фоновая горутина — запись перехода (не тормозит ответ):
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
5. v2: Accept без text/html (curl, боты)?
   ├── да  → 302 Found, Location: original_url → nginx → клиент (конец)
   └── нет → выбор баннера и рендер страницы перехода, шаг 6
6. v2: реклама (app/v2/ads.go)
   a) перечитываем static/ads/ad_images.txt (image|link|weight) —
      правки пула применяются без рестарта
   b) pickAd — взвешенный выбор баннера по полю weight
   c) Record(impression) — счётчики в буфер в памяти
   d) подпись из static/ads/ad_texts.txt — случайная строка
   e) renderRoutingPage: шаблон ROUTING_PAGE_FILE с диска, иначе встроенный
      routingPageTmpl (embed). Подстановки: TargetURL, AdImage, AdLink, AdText
   f) 200 OK, Content-Type: text/html; Cache-Control: no-store;
      Referrer-Policy: no-referrer; X-Robots-Tag: noindex, nofollow
7. Страница в браузере: кольцевой таймер 5 с → кнопка «Продолжить»
   → window.location = TargetURL (переход на оригинальный URL)
8. Клик по баннеру: GET /ad-click/{ad_key}?link=<id> → nginx → v2
   a) валидация ключа, поиск баннера в актуальном ad_images.txt
   b) Record(click) — и по ссылке, и сквозной
   c) 302 Found на сайт рекламодателя
```

**Ключевые детали:**

- **Кэш редиректа**: `url:{short_code}` → `<link_id>:<original_url>`, TTL 3600 с. Удаление ссылки делает `DEL` ключа — после этого кэш не «воскрешает» удалённую ссылку. В кэш кладётся и `link_id`, чтобы не делать лишний `SELECT` ради метрик рекламы.
- **Кэш гео**: `geo:{ip}` → ISO-код страны, TTL 86400 с (24 ч). Снижает число обращений к ip-api.com (бесплатный лимит — 45 запросов/мин с одного IP сервера) до ~«число уникальных IP в сутки».
- **Честность данных**: запись в `analytics` идёт горутиной после отправки ответа, потеря перехода при падении контейнера допустима — это осознанный компромисс скорости и точности.
- **Неблокирующий ответ**: даже при полностью лежащем гео-API переход не задержится дольше 2 сек (таймаут HTTP-клиента), страна будет `unknown`. Запись гео идёт в горутине и ответ не ждёт.
- **Метрики рекламы не пишутся в SQLite на каждый показ**: `AdStatsRecorder` копит счётчики в `map` под мьютексом и сбрасывает пачками раз в `AD_FLUSH_INTERVAL_SECONDS` (по умолчанию 5 с), а также при остановке контейнера по SIGINT/SIGTERM. Файл SQLite общий с legacy-сервисом, и лишние транзакции в горячем пути давали бы contention (см. раздел 9). События, попавшие в буфер во время flush, попадут в следующий пакет — потеря счётчика на пакете допустима.
- **Клик по баннеру идёт через сервис**, а не напрямую на сайт рекламодателя: без этого сервер физически не узнаёт о клике и CTR не измеряется.
- **Реклама не показывается при 404**: баннер выбирается уже после того, как ссылка найдена в БД.

---

## 7. Конфигурация Docker

### 7.1. docker-compose.yml

Четыре сервиса, всё на Alpine. Наружу опубликован только nginx :80 (единственный entrypoint); `v2`, `app` и redis доступны лишь во внутренней сети.

> **Важно: `v2` и `app` смонтированы на один том `sqlite-data` и пишут в один файл `/data/urlshortener.db`.** Сейчас это безопасно: nginx не направляет трафик на legacy-`app`, поэтому писатель в БД фактически один. Но конфигурация не защищает от возврата к одновременной записи — см. риск №9 в разделе 9. Если legacy-реализация понадобится для нагрузки, её нужно либо вынести на отдельный файл БД, либо запускать в режиме «только чтение».

```yaml
services:
  app:                                          # LEGACY, трафика не получает
    build: .                                    # Dockerfile: python:3.12-alpine
    container_name: urlshortener-app
    restart: unless-stopped
    environment:
      DATABASE_PATH: /data/urlshortener.db       # тот же файл, что и у v2 — см. предупреждение выше
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
      # 127.0.0.1, а не localhost: BusyBox-wget резолвит localhost в ::1,
      # а uvicorn слушает только IPv4 (0.0.0.0) — был connection refused
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8000/api/v1/health"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s

  v2:                                           # АКТИВНЫЙ бэкенд
    build: ./app/v2                             # multi-stage, CGO_ENABLED=0, non-root
    container_name: urlshortener-v2
    restart: unless-stopped
    environment:
      DATABASE_PATH: /data/urlshortener.db       # общий файл с app — см. предупреждение выше
      REDIS_URL: redis://redis:6379/0
      GEO_API_URL: http://ip-api.com/json/{ip}?fields=status,countryCode
      CACHE_TTL_SECONDS: "3600"
      GEO_CACHE_TTL_SECONDS: "86400"
      GEO_TIMEOUT_SECONDS: "2"
      BASE_URL: http://localhost                 # для сборки short_url в ответах
      AD_IMAGES_FILE: /static/ads/ad_images.txt  # пул баннеров: image|link|weight
      AD_TEXTS_FILE: /static/ads/ad_texts.txt    # подписи под баннером
      AD_FLUSH_INTERVAL_SECONDS: "5"             # период сброса буфера метрик в SQLite
      ROUTING_PAGE_FILE: /templates/routing_page.html
    volumes:
      - sqlite-data:/data
      - ./static:/static:ro                      # баннеры и их тексты — только чтение
      - ./app/v2:/templates:ro                   # шаблон страницы перехода — без пересборки
    depends_on:
      redis:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8000/api/v2/health"]
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
      - ./static:/usr/share/nginx/html:ro       # статику и баннеры отдаёт сам nginx
    depends_on:
      v2:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1/api/v2/health"]
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

    # 1. Короткие ссылки (7 символов) — на Go-v2, до try_files.
    #    Regex-локация выигрывает у префиксной "/", поэтому реальная
    #    статика (/css/style.css) сюда не попадает.
    location ~ "^/[0-9a-zA-Z]{7}$" {
        proxy_pass http://backend_v2;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }

    # 2. Клик по баннеру — счётчик клика + 302 на рекламодателя.
    #    Без этой локации /ad-click/... ушёл бы в статику.
    location /ad-click/ {
        proxy_pass http://backend_v2;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }

    # 3. REST API v2 — фронтенд ходит только сюда
    location /api/v2/ {
        proxy_pass http://backend_v2;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }

    # 4. REST API v1 — только legacy-Python, для сверки поведения
    location /api/v1-legacy/ {
        proxy_pass http://backend_v1/api/v1/;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }

    # 5. Статика фронтенда и баннеры — с диска, без участия бэкенда
    location / {
        root /usr/share/nginx/html;
        index index.html;
        try_files $uri $uri/ /index.html;
    }
}
```

Регулярка `^/[0-9a-zA-Z]{7}$` в nginx отсекает заведомо невалидные коды ещё до приложения; настоящие файлы статики (например `/css/style.css`) под локацию не попадают — она перекрывается `location /` только для несовпавших URI (nginx выбирает точное совпадение regex первым при таком порядке).

### 7.3. Переменные окружения (`.env.example`)

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `DATABASE_PATH` | `/data/urlshortener.db` | путь к файлу SQLite на томе. **Одинаков для `v2` и `app`** |
| `REDIS_URL` | `redis://redis:6379/0` | подключение к Redis |
| `GEO_API_URL` | `http://ip-api.com/json/{ip}?fields=status,countryCode` | шаблон гео-запроса |
| `GEO_TIMEOUT_SECONDS` | `2` | таймаут внешнего гео-запроса |
| `CACHE_TTL_SECONDS` | `3600` | TTL кэша редиректов |
| `GEO_CACHE_TTL_SECONDS` | `86400` | TTL кэша гео (24 ч) |
| `BASE_URL` | `http://localhost` | база для `short_url` в ответах API |
| `AD_IMAGES_FILE` | `/static/ads/ad_images.txt` | пул баннеров: `<файл>\|<ссылка>\|<вес>` |
| `AD_TEXTS_FILE` | `/static/ads/ad_texts.txt` | подписи под баннером, по строке на файл |
| `AD_FLUSH_INTERVAL_SECONDS` | `5` | период сброса буфера метрик рекламы в SQLite, сек |
| `ROUTING_PAGE_FILE` | `/templates/routing_page.html` | шаблон страницы перехода с диска |

### 7.4. Тесты

Тесты `app/v2` не требуют Docker: SQLite — во временном файле, Redis поднимается внутри процесса через `miniredis`, шаблон страницы — реальный файл. Запуск:

```cmd
cd app/v2
go test ./...
```

Покрыто: генерация и валидация `short_code`, разбор `ad_images.txt` и веса баннеров, взвешенный выбор, CRUD-эндпоинты с кодами ошибок, `prefersJSON`, определение приватного IP и кэш гео, экранирование в шаблоне, учёт показов/кликов, flush буфера (в том числе при остановке) и агрегаты.

---

## 8. Фронтенд

Статические файлы без сборки, отдаёт nginx. Один экран + одна модалка + страница перехода.

### 8.1. Главная страница (`static/index.html`)

- **Форма**: поле «Вставьте длинный URL» + кнопка «Сократить!».
- **Результат**: короткий URL в моноширинном блоке, кнопки «Копировать» (Clipboard API), «Статистика» (открывает модалку), «Удалить».
- **Панели**: страница построена как «окна» — заголовок панели с кнопками-декорациями, bevel-рамки, сгруппированные блоки в духе `fieldset`. Эпоха передана структурой и фактурой, а не копированием оформления Windows XP: никаких сторонних шрифтов и картинок-обоев, только системный моноширинный и встроенные SVG-паттерны, поэтому фронтенд остаётся статикой без сборки.
- **Палитра** — приглушённые холодные тона из задания: slate `#2f4f4f`, steel blue `#4682b4`, light steel `#b0c4de` на тёмном фоне, базовый фон `#101418`. Контраст текста к фону — не ниже WCAG AA.
- **Байки про программистов** в сайдбаре: ASCII-арт «WORKS ON MY MACHINE» и байка про десяток батонов. Это визитка проекта, а не декорация.
- **Анимации полей ввода**: placeholder «печатается» посимвольно (`setInterval`, 90 мс на символ), при фокусе рамка «загорается» холодным свечением (`box-shadow` + `transition`), мигающий caret, label подсвечивается при фокусе поля. Сторонних библиотек нет.
- **`prefers-reduced-motion: reduce`** отключает свечение и мигающий caret, а JS дополнительно не запускает посимвольную печать placeholder (вместо неё показывается готовый текст); смена настройки без перезагрузки обрабатывается через `matchMedia().addEventListener('change')`. Доступность не приносится в жертву стилю: реальные `<label>`, `aria-live` на результате, видимые фокус-кольца.

### 8.2. Модалка статистики

- Открывается кнопкой «Статистика» у созданной ссылки; в модалке работает «Обновить» (повторный fetch) и просмотр статистики **чужой** ссылки по введённому 7-символьному коду.
- Содержимое: короткий URL, исходный URL, дата создания, крупный счётчик переходов, таблица «Страна — Переходов» (по убыванию), счётчики показов и кликов рекламы для этой ссылки.
- Закрытие: крестик, клик по фону, `Esc`. При открытии фокус уходит внутрь модалки, при закрытии возвращается на кнопку.

### 8.3. JS-код вызова API (`static/js/app.js`)

Ванильный JS, без зависимостей. Пути — только `/api/v2/`:

```js
// 1. Создание ссылки
const res = await fetch('/api/v2/links', {
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
const stats = await fetch(`/api/v2/links/${shortCode}/stats`);
if (stats.ok) renderStats(await stats.json());   // { clicks, countries: [...] }

// 3. Реклама по этой ссылке
const ads = await fetch(`/api/v2/links/${shortCode}/ads`);
// { impressions, clicks, ctr }

// 4. Удаление
const del = await fetch(`/api/v2/links/delete/${shortCode}`, { method: 'DELETE' });

// 5. Копирование в буфер
await navigator.clipboard.writeText(shortUrl);
```

Роутинг на стороне сервера для UI не нужен: `GET /` отдаёт `index.html` из статики nginx, короткая ссылка `/aB3xK9z` перехватывается regex-локацией до статики и уходит на страницу перехода.

### 8.4. Страница перехода (`app/v2/routing_page.html`)

Рендерится на сервере Go (раздел 4.2). Требования к оформлению те же, что у главной страницы: та же палитра и те же токены, поэтому переход по ссылке не выглядит как продукт из другого репозитория. Элементы: кольцевой таймер на 5 секунд, рекламный баннер, подпись, кнопка «Продолжить», которая уводит на `TargetURL`.

Шаблон подхватывается с диска (`ROUTING_PAGE_FILE`) и применяется без пересборки; при ошибке чтения используется встроенная копия из бинарника (`go:embed`).

---

## 9. Риски и компромиссы

| # | Риск / компромисс | Митигация / принятое решение |
|---|---|---|
| 1 | **SQLite под параллельной записью** — один писатель одновременно; вставки analytics при всплеске переходов могут конфликтовать | `PRAGMA journal_mode=WAL` (читатели не блокируют писателя), `busy_timeout=5000` (мягкое ожидание блокировки), `synchronous=NORMAL`, `SetMaxOpenConns(1)` в Go. Запись переходов идёт горутиной после ответа клиенту, метрики рекламы — пачками раз в 5 с. При типичной нагрузке пет-проекта (десятки переходов/сек) узкого места нет; вертикальный рост — переход на PostgreSQL. |
| 2 | **Два сервиса пишут в один файл SQLite** (`v2` и legacy `app` на томе `sqlite-data`) | Зафиксировано в разделе 7.1. Сейчас безопасно: nginx не направляет трафик на `app`, писатель фактически один. Если legacy вернётся в бой: общий `DATABASE_PATH` обязателен, оба сервиса должны использовать одинаковые PRAGMA, а `app` — быть либо выведен из состава, либо ограничен чтением. Проявилась бы гонка как `database is locked` по истечении `busy_timeout`, а не как порча данных: WAL гарантирует целостность, но не многописателей. |
| 3 | **Честность аналитики без регистрации**: статистика и удаление доступны любому, знающему `short_code`; повторы одного пользователя считаются; боты не фильтруются | Осознанный компромисс анонимного сервиса. Пространство 58⁷ кодов делает угадывание чужих ссылок практически невозможным. Ограничение зафиксировано: фильтрация краулеров по `User-Agent` не реализована. |
| 4 | **DELETE без владения** — любой может удалить ссылку, узнав код | Принято в рамках ТЗ (сервис анонимный). Смягчение: физическое удаление + каскад + инвалидация кэша; перебор 58⁷ вариантов экономически бессмыслен. |
| 5 | **Внешнее гео-API (ip-api.com)**: лимит 45 запросов/мин, недоступность, бесплатный тариф только по HTTP (без TLS) | Кэш `geo:{ip}` в Redis на 24 ч радикально снижает число вызовов (один вызов на уникальный IP в сутки). Таймаут 2 с, при сбое — страна `unknown`, переход фиксируется всегда. Приватные IP — `local` без внешних вызовов. Через HTTP уходит только IP-адрес, никаких URL пользователей. Fallback-цепочка: Redis → ip-api.com → `'unknown'`; при необходимости расширения — заменяемый `GEO_API_URL`. |
| 6 | **Реклама на пути перехода** — пользователь видит баннер перед открытием ссылки | Требование варианта задания №10, поэтому осознанное решение. Смягчения: задержка фиксирована 5 с и не растёт; баннеры и подписи берутся из файлов и снимаются без пересборки; для клиентов без `Accept: text/html` отдаётся обычный `302` без рекламы; `X-Robots-Tag: noindex, nofollow` и `Referrer-Policy: no-referrer`, чтобы страница не индексировалась и не утекала referrer. Претензии пользователей и блокировщики рекламы — ожидаемый побочный эффект монетизации. |
| 7 | **CTR зависит от клика через сервис** — блокировщик или ручная вставка ссылки обходят учёт | Измеримость важнее абсолютной точности: агрегаты отдаются как приблизительные. Обход учёта не даёт рекламодателю инструмента давления на сервис, а доля «честных» кликов для pet-проекта достаточна для оценки эффективности баннера. |
| 8 | **Потеря событий при падении контейнера** — буфер метрик в памяти, запись перехода в горутине | Компенсировано частично: `signal.NotifyContext` даёт финальный flush буфера по SIGINT/SIGTERM, а `docker stop` шлёт именно SIGTERM. Аварийный `kill -9` — единственный случай полной потери, приемлемый для счётчиков. Полная надёжность потребовала бы очереди — исключена по стеку (без AMQP). |
| 9 | **Нет дедупликации URL** — один и тот же длинный URL можно сокращать многократно | Упрощение: без пользователей некому «владеть» ссылкой; консолидация сломала бы раздельную статистику. |
| 10 | **Redis as cache, не source of truth** — потеря кэша = промахи в SQLite | `maxmemory-policy allkeys-lru`, AOF-персистентность на томе `redis-data` для тёплого старта. Деградация производительная, не функциональная. |
| 11 | **Две реализации API в репозитории** — риск расхождения поведения и правок в двух местах | `app/v2` — единственная боевая реализация; `app/` (Python) — legacy без изменений, доступна под `/api/v1-legacy/` только для сверки. Переписывание на Go обосновано скоростью и single-binary сборкой; полное удаление legacy — отдельная задача. |

---

## Приложение: сверка с требованиями Readme

| Требование | Где реализовано |
|---|---|
| Создание короткой ссылки | `POST /api/v2/links` (раздел 4.1) |
| Переход по короткой ссылке | `GET /{short_code}` (разделы 4.2, 6) — страница рекламы для браузера, `302` для клиентов без `text/html` |
| Аналитика: переходы, геолокация | `GET /api/v2/links/{short_code}/stats` (раздел 4.6), поток 6 |
| Удаление | `DELETE /api/v2/links/delete/{short_code}` (раздел 4.7) |
| Без регистрации | анонимные эндпоинты, компромиссы 3–4 |
| Монетизация: реклама (вариант №10) | разделы 4.2 (показ), 4.3 (клик), 4.4–4.5 (агрегаты), поток 6 |
| UI 2000-х, модалка, мем, холодные тона, анимация | раздел 8 |
| Alpine, Redis, SQLite, Go, reverse proxy | разделы 1, 7 |
| AMQP не нужен | нагрузка не требуется; асинхронность — горутины и буфер с flush |