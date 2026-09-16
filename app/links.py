"""Роутер ссылок: создание, редирект, статистика, удаление.

Спецификация — docs/ARCHITECTURE.md, раздел 4; поток редиректа — раздел 6.
Маршруты объявлены полными путями; редирект `GET /{short_code}`
регистрируется в FastAPI после `/api/...`, а встроенные `/docs`,
`/openapi.json` добавляются ещё в конструкторе приложения — конфликтов нет.

Лимит переходов (вариант 9): при создании ссылке можно задать max_clicks=N.
Как только счётчик analytics достигает N, очередной переход получает
410 Gone («лимит исчерпан»), а ссылка удаляется: analytics уходит каскадом,
кэш редиректа инвалидируется. Дальнейшие запросы — обычный 404.
"""

import sqlite3
from urllib.parse import urlparse

from fastapi import APIRouter, BackgroundTasks, HTTPException, Request, status
from fastapi.responses import HTMLResponse, RedirectResponse, Response

from app.cache import cache
from app.config import settings
from app.database import db
from app.geo import extract_client_ip, resolve_country
from app.models import (
    CountryClicks,
    DeleteResponse,
    LinkCreate,
    LinkResponse,
    StatsResponse,
)
from app.shortcuts import MAX_INSERT_ATTEMPTS, generate_short_code, is_valid_short_code

router = APIRouter(tags=["links"])

MAX_URL_LENGTH = 2048
ALLOWED_SCHEMES = ("http", "https")

# Страница 410 для браузеров (API-клиенты получают JSON с тем же смыслом).
# __CODE__ и __MAX__ заменяются фактическими значениями при ответе.
LIMIT_EXCEEDED_HTML = """<!DOCTYPE html>
<html lang="ru">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>410 &mdash; лимит переходов исчерпан :: ShortURL 2000</title>
    <style>
        body {
            margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
            background: linear-gradient(180deg, #37565c 0%, #2f4f4f 55%, #263f47 100%) fixed;
            color: #1d3434; font-family: Tahoma, Verdana, "Segoe UI", Arial, sans-serif; font-size: 14px;
        }
        .win {
            width: min(480px, 92vw); background: #b0c4de; border: 2px solid;
            border-color: #cddced #2f4f4f #2f4f4f #cddced;
            box-shadow: 3px 3px 0 rgba(0, 0, 0, .35);
        }
        .caption {
            padding: 4px 8px; font-weight: bold; font-size: 13px; color: #eaf2fa;
            text-shadow: 1px 1px 0 rgba(0, 0, 0, .5);
            background: repeating-linear-gradient(180deg, #4682b4 0 2px, #315f85 2px 4px);
            border-bottom: 1px solid #315f85;
        }
        .body { padding: 16px 20px 20px; text-align: center; }
        .code { font-family: Consolas, "Courier New", monospace; font-weight: bold; }
        h1 {
            font-family: Consolas, "Courier New", monospace; font-size: 40px; letter-spacing: 4px;
            color: #f2f7fc; text-shadow: 2px 2px 0 #223a3a; margin: 12px 0 4px;
        }
        p { margin: 10px 0; }
        .note { font-size: 11px; color: #3d5a6b; }
    </style>
</head>
<body>
    <div class="win">
        <div class="caption">&#9888; ShortURL 2000 &mdash; [окно ошибки]</div>
        <div class="body">
            <h1>410</h1>
            <p>Лимит переходов для ссылки <span class="code">/__CODE__</span> исчерпан
               (максимум был <span class="code">__MAX__</span>).</p>
            <p>Ссылка удалена вместе со своей статистикой.</p>
            <p class="note">Всё конечное &mdash; даже короткие ссылки. &copy; ShortURL 2000 Labs</p>
        </div>
    </div>
</body>
</html>
"""


def build_short_url(short_code: str) -> str:
    """Короткий URL для ответов: BASE_URL + /{code} (слэш не дублируем)."""
    return f"{settings.base_url.rstrip('/')}/{short_code}"


def url_cache_key(short_code: str) -> str:
    """Ключ кэша редиректа: url:{short_code} -> '{link_id}:{max_clicks|0}:{original_url}'.

    link_id нужен для фоновой записи analytics даже при кэш-хите, max_clicks —
    для проверки лимита переходов (0 = лимит не задан), поэтому храним
    составное значение и восстанавливаем без второго похода в БД.
    Значения старого формата '{link_id}:{url}' парсинг не проходят
    (второе поле не цифра) и трактуются как промах — кэш прогреется заново.
    """
    return f"url:{short_code}"


async def purge_link(short_code: str) -> int:
    """Удалить ссылку (analytics уйдёт каскадом) и инвалидировать кэш редиректа.

    Возвращает число удалённых строк links (0 — ссылки не было).
    """
    deleted_rows = await db.execute(
        "DELETE FROM links WHERE short_code = ?", (short_code,)
    )
    await cache.delete(url_cache_key(short_code))
    return deleted_rows


def limit_exceeded_response(short_code: str, max_clicks: int, request: Request) -> Response:
    """410 Gone: HTML-страница для браузера, JSON detail — для API-клиентов."""
    if "text/html" in (request.headers.get("accept") or "").lower():
        return HTMLResponse(
            content=LIMIT_EXCEEDED_HTML.replace("__CODE__", short_code)
            .replace("__MAX__", str(max_clicks)),
            status_code=status.HTTP_410_GONE,
        )
    raise HTTPException(
        status_code=status.HTTP_410_GONE,
        detail=f"Лимит переходов исчерпан ({max_clicks} из {max_clicks}), ссылка удалена",
    )


async def record_click(link_id: int, ip: str) -> None:
    """Фоновая задача редиректа: страна (кэш -> ip-api -> 'unknown') + INSERT.

    Выполняется после отправки 302 клиенту: потеря одного клика при сбое
    допустима (docs/ARCHITECTURE.md, риск №7).
    """
    country = await resolve_country(ip)
    try:
        await db.execute(
            "INSERT INTO analytics (link_id, ip_address, country) VALUES (?, ?, ?)",
            (link_id, ip, country),
        )
    except sqlite3.Error:
        pass  # аналитика не должна ломать сервис


# ---------------------------------------------------------------- POST /api/v1/links
@router.post(
    "/api/v1/links",
    response_model=LinkResponse,
    status_code=status.HTTP_201_CREATED,
    summary="Создать короткую ссылку",
)
async def create_link(payload: LinkCreate) -> LinkResponse:
    original_url = payload.original_url

    # 400 по бизнес-правилам (не-URL и пустое поле отсёк pydantic -> 422)
    if urlparse(original_url).scheme.lower() not in ALLOWED_SCHEMES:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="Поддерживаются только схемы http и https",
        )
    if len(original_url) > MAX_URL_LENGTH:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="URL не может быть длиннее 2048 символов",
        )

    # Коллизии ловит UNIQUE-индекс: INSERT -> IntegrityError -> перегенерация.
    # «SELECT перед INSERT» был бы гонкой, атомарность гарантирует сам индекс.
    for _ in range(MAX_INSERT_ATTEMPTS):
        short_code = generate_short_code()
        try:
            await db.execute(
                "INSERT INTO links (short_code, original_url, max_clicks) VALUES (?, ?, ?)",
                (short_code, original_url, payload.max_clicks),
            )
            break
        except sqlite3.IntegrityError:
            continue
    else:
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail="Не удалось сгенерировать уникальный код, попробуйте ещё раз",
        )

    row = await db.fetch_one(
        "SELECT created_at FROM links WHERE short_code = ?", (short_code,)
    )
    return LinkResponse(
        short_code=short_code,
        short_url=build_short_url(short_code),
        original_url=original_url,
        created_at=row["created_at"] if row else "",
        max_clicks=payload.max_clicks,
        clicks_left=payload.max_clicks,  # ссылка новая — все переходы впереди
    )


# ------------------------------------------------------- GET /{short_code} (redirect)
@router.get(
    "/{short_code}",
    summary="Редирект на оригинальный URL (302)",
    response_class=RedirectResponse,
)
async def redirect_to_original(
    short_code: str,
    request: Request,
    background_tasks: BackgroundTasks,
) -> Response:
    # Неверный формат (не 7 символов base62) — сразу 404 без обращения к БД/кэшу
    if not is_valid_short_code(short_code):
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Ссылка не найдена")

    link_id: int | None = None
    max_clicks: int | None = None
    original_url: str | None = None

    # 1) Кэш Redis: url:{code} -> "{link_id}:{max_clicks|0}:{original_url}"
    cached = await cache.get(url_cache_key(short_code))
    if cached is not None:
        link_part, sep1, remainder = cached.partition(":")
        max_part, sep2, url_part = remainder.partition(":")
        if sep1 and sep2 and link_part.isdigit() and max_part.isdigit():
            link_id, original_url = int(link_part), url_part
            max_clicks = int(max_part) or None  # 0 в кэше = лимит не задан

    # 2) Промах (или кэш недоступен/битый/старый формат) -> SQLite, затем прогрев
    if original_url is None:
        row = await db.fetch_one(
            "SELECT id, original_url, max_clicks FROM links WHERE short_code = ?",
            (short_code,),
        )
        if row is None:
            raise HTTPException(
                status_code=status.HTTP_404_NOT_FOUND, detail="Ссылка не найдена"
            )
        link_id = row["id"]
        original_url = row["original_url"]
        max_clicks = row["max_clicks"]
        await cache.setex(
            url_cache_key(short_code),
            settings.cache_ttl_seconds,
            f"{link_id}:{max_clicks or 0}:{original_url}",
        )

    # 3) Лимит переходов (вариант 9): счётчик analytics достиг max_clicks ->
    #    410 «лимит исчерпан» + удаление ссылки (каскад + инвалидация кэша).
    #    COUNT делается только для ссылок с лимитом; для остальных — ноль запросов.
    if max_clicks is not None:
        total_row = await db.fetch_one(
            "SELECT COUNT(*) AS cnt FROM analytics WHERE link_id = ?", (link_id,)
        )
        if (total_row["cnt"] if total_row else 0) >= max_clicks:
            await purge_link(short_code)
            return limit_exceeded_response(short_code, max_clicks, request)

    # 4) Фиксация перехода после ответа (не тормозит редирект). Переход,
    #    наткнувшийся на исчерпанный лимит, не учитывается — ссылка удалена.
    client_ip = extract_client_ip(
        request.headers.get("x-forwarded-for"),
        request.headers.get("x-real-ip"),
        request.client.host if request.client else None,
    )
    background_tasks.add_task(record_click, link_id, client_ip)

    # 302, а не 301: браузеры кэшируют 301 навсегда и аналитика умирает
    return RedirectResponse(
        url=original_url,
        status_code=status.HTTP_302_FOUND,
        headers={"Cache-Control": "no-store"},
    )


# ------------------------------------------- GET /api/v1/links/{short_code}/stats
@router.get(
    "/api/v1/links/{short_code}/stats",
    response_model=StatsResponse,
    summary="Статистика переходов по ссылке",
)
async def get_stats(short_code: str) -> StatsResponse:
    link = await db.fetch_one(
        "SELECT id, short_code, original_url, created_at, max_clicks "
        "FROM links WHERE short_code = ?",
        (short_code,),
    )
    if link is None:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Ссылка не найдена")

    total_row = await db.fetch_one(
        "SELECT COUNT(*) AS cnt FROM analytics WHERE link_id = ?", (link["id"],)
    )
    clicks = total_row["cnt"] if total_row else 0

    country_rows = await db.fetch_all(
        """
        SELECT a.country, COUNT(*) AS cnt
        FROM analytics a
        WHERE a.link_id = ?
        GROUP BY a.country
        ORDER BY cnt DESC
        """,
        (link["id"],),
    )
    countries = [
        CountryClicks(country=row["country"] or "unknown", count=row["cnt"])
        for row in country_rows
    ]

    max_clicks = link["max_clicks"]
    return StatsResponse(
        short_code=link["short_code"],
        original_url=link["original_url"],
        created_at=link["created_at"],
        clicks=clicks,
        countries=countries,
        max_clicks=max_clicks,
        clicks_left=max(0, max_clicks - clicks) if max_clicks is not None else None,
    )


# ------------------------------------- DELETE /api/v1/links/delete/{short_code}
@router.delete(
    "/api/v1/links/delete/{short_code}",
    response_model=DeleteResponse,
    summary="Удалить ссылку и её аналитику",
)
async def delete_link(short_code: str) -> DeleteResponse:
    if await purge_link(short_code) == 0:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Ссылка не найдена")
    return DeleteResponse(short_code=short_code, deleted=True)
