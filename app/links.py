"""Роутер ссылок: создание, редирект, статистика, удаление.

Спецификация — docs/ARCHITECTURE.md, раздел 4; поток редиректа — раздел 6.
Маршруты объявлены полными путями; редирект `GET /{short_code}`
регистрируется в FastAPI после `/api/...`, а встроенные `/docs`,
`/openapi.json` добавляются ещё в конструкторе приложения — конфликтов нет.
"""

import sqlite3
from urllib.parse import urlparse

from fastapi import APIRouter, BackgroundTasks, HTTPException, Request, status
from fastapi.responses import RedirectResponse

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


def build_short_url(short_code: str) -> str:
    """Короткий URL для ответов: BASE_URL + /{code} (слэш не дублируем)."""
    return f"{settings.base_url.rstrip('/')}/{short_code}"


def url_cache_key(short_code: str) -> str:
    """Ключ кэша редиректа: url:{short_code} -> '{link_id}:{original_url}'.

    link_id нужен фоновой записи analytics даже при кэш-хите, поэтому
    храним составное значение и восстанавливаем без второго похода в БД.
    """
    return f"url:{short_code}"


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
                "INSERT INTO links (short_code, original_url) VALUES (?, ?)",
                (short_code, original_url),
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
) -> RedirectResponse:
    # Неверный формат (не 7 символов base62) — сразу 404 без обращения к БД/кэшу
    if not is_valid_short_code(short_code):
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Ссылка не найдена")

    link_id: int | None = None
    original_url: str | None = None

    # 1) Кэш Redis: url:{code} -> "{link_id}:{original_url}"
    cached = await cache.get(url_cache_key(short_code))
    if cached is not None:
        prefix, sep, rest = cached.partition(":")
        if sep and prefix.isdigit():  # защита от повреждённого значения
            link_id, original_url = int(prefix), rest

    # 2) Промах (или кэш недоступен/битый) -> SQLite, затем прогрев кэша
    if original_url is None:
        row = await db.fetch_one(
            "SELECT id, original_url FROM links WHERE short_code = ?", (short_code,)
        )
        if row is None:
            raise HTTPException(
                status_code=status.HTTP_404_NOT_FOUND, detail="Ссылка не найдена"
            )
        link_id = row["id"]
        original_url = row["original_url"]
        await cache.setex(
            url_cache_key(short_code),
            settings.cache_ttl_seconds,
            f"{link_id}:{original_url}",
        )

    # 3) Фиксация перехода после ответа (не тормозит редирект)
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
        "SELECT id, short_code, original_url, created_at FROM links WHERE short_code = ?",
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

    return StatsResponse(
        short_code=link["short_code"],
        original_url=link["original_url"],
        created_at=link["created_at"],
        clicks=clicks,
        countries=countries,
    )


# ------------------------------------- DELETE /api/v1/links/delete/{short_code}
@router.delete(
    "/api/v1/links/delete/{short_code}",
    response_model=DeleteResponse,
    summary="Удалить ссылку и её аналитику",
)
async def delete_link(short_code: str) -> DeleteResponse:
    deleted_rows = await db.execute(
        "DELETE FROM links WHERE short_code = ?", (short_code,)
    )
    if deleted_rows == 0:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Ссылка не найдена")

    # analytics удалена каскадом (ON DELETE CASCADE + PRAGMA foreign_keys=ON);
    # инвалидируем кэш, чтобы удалённая ссылка не «воскресла» из Redis
    await cache.delete(url_cache_key(short_code))
    return DeleteResponse(short_code=short_code, deleted=True)
