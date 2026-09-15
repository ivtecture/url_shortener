"""Роутер ссылок: создание с TTL, редирект, статистика, удаление.

Новое для ветки temp-links:
- POST /api/v1/links принимает ttl_seconds (60..604800, дефолт из настроек);
- при создании считается expires_at (UTC);
- редирект проверяет срок: просроченная ссылка -> 410 Gone;
- кэш-ключ живёт не дольше самой ссылки (min(cache_ttl, до истечения));
- фоновая чистка удаляет просроченные ссылки вместе с аналитикой (каскад).
"""

import sqlite3
from datetime import datetime, timedelta, timezone
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


def utc_now() -> datetime:
    """Текущее время UTC (наивное — так же пишет datetime('now') в SQLite)."""
    return datetime.now(timezone.utc).replace(tzinfo=None)


def format_ts(dt: datetime) -> str:
    """Единый формат времён: 'YYYY-MM-DD HH:MM:SS' UTC."""
    return dt.strftime("%Y-%m-%d %H:%M:%S")


def build_short_url(short_code: str) -> str:
    return f"{settings.base_url.rstrip('/')}/{short_code}"


def url_cache_key(short_code: str) -> str:
    """url:{code} -> '{link_id}:{original_url}:{expires_at}'."""
    return f"url:{short_code}"


async def record_click(link_id: int, ip: str) -> None:
    """Фоновая запись перехода: страна + INSERT (сбой не роняет сервис)."""
    country = await resolve_country(ip)
    try:
        await db.execute(
            "INSERT INTO analytics (link_id, ip_address, country) VALUES (?, ?, ?)",
            (link_id, ip, country),
        )
    except sqlite3.Error:
        pass


# ---------------------------------------------------------------- POST /api/v1/links
@router.post(
    "/api/v1/links",
    response_model=LinkResponse,
    status_code=status.HTTP_201_CREATED,
    summary="Создать временную короткую ссылку",
)
async def create_link(payload: LinkCreate) -> LinkResponse:
    original_url = str(payload.original_url)

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

    # TTL: не указан -> дефолт; выход за границы -> 400 (сверху страховка pydantic)
    ttl = payload.ttl_seconds if payload.ttl_seconds is not None else settings.default_ttl_seconds
    if ttl < settings.min_ttl_seconds or ttl > settings.max_ttl_seconds:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail=(
                f"ttl_seconds должен быть от {settings.min_ttl_seconds} "
                f"до {settings.max_ttl_seconds} секунд"
            ),
        )

    now = utc_now()
    expires_at = now + timedelta(seconds=ttl)
    expires_str = format_ts(expires_at)

    # Коллизии ловит UNIQUE-индекс: перегенерация кода до MAX_INSERT_ATTEMPTS раз
    for _ in range(MAX_INSERT_ATTEMPTS):
        short_code = generate_short_code()
        try:
            await db.execute(
                "INSERT INTO links (short_code, original_url, created_at, expires_at) "
                "VALUES (?, ?, ?, ?)",
                (short_code, original_url, format_ts(now), expires_str),
            )
            break
        except sqlite3.IntegrityError:
            continue
    else:
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail="Не удалось сгенерировать уникальный код, попробуйте ещё раз",
        )

    return LinkResponse(
        short_code=short_code,
        short_url=build_short_url(short_code),
        original_url=original_url,
        expires_at=expires_str,
    )


# ------------------------------------------------------- GET /{short_code} (редирект)
@router.get(
    "/{short_code}",
    summary="Редирект на оригинальный URL",
    response_class=RedirectResponse,
)
async def redirect(short_code: str, background_tasks: BackgroundTasks,
                   request: Request) -> RedirectResponse:
    if not is_valid_short_code(short_code):
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Ссылка не найдена")

    link_id: int | None = None
    original_url: str | None = None
    expires_str: str | None = None

    # 1) Кэш: 'link_id:original_url:expires_at'
    cached = await cache.get(url_cache_key(short_code))
    if cached:
        parts = cached.split(":", 2)
        if len(parts) == 3 and parts[0].isdigit():
            link_id, original_url, expires_str = int(parts[0]), parts[1], parts[2]

    # 2) Промах -> SQLite, затем прогрев кэша
    if original_url is None:
        row = await db.fetch_one(
            "SELECT id, original_url, expires_at FROM links WHERE short_code = ?",
            (short_code,),
        )
        if row is None:
            raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Ссылка не найдена")
        link_id = row["id"]
        original_url = row["original_url"]
        expires_str = row["expires_at"]

    # 3) Проверка срока жизни — главное отличие ветки temp-links
    def parse_expires(value: str | None) -> datetime | None:
        try:
            return datetime.strptime(value, "%Y-%m-%d %H:%M:%S")
        except (TypeError, ValueError):
            return None

    expires_dt = parse_expires(expires_str)
    if expires_dt is not None and expires_dt <= utc_now():
        # Кэш просроченной ссылки больше не нужен
        await cache.delete(url_cache_key(short_code))
        raise HTTPException(
            status_code=status.HTTP_410_GONE,
            detail="Срок жизни ссылки истёк",
        )

    # Прогрев: ключ живёт не дольше min(cache_ttl, оставшегося срока ссылки)
    if cached is None:
        remaining = settings.cache_ttl_seconds
        if expires_dt is not None:
            remaining = max(int((expires_dt - utc_now()).total_seconds()), 1)
        await cache.setex(
            url_cache_key(short_code),
            max(min(settings.cache_ttl_seconds, remaining), 1),
            f"{link_id}:{original_url}:{expires_str}",
        )

    # 4) Фиксация перехода после ответа (не тормозит редирект)
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
        "SELECT id, short_code, original_url, created_at, expires_at "
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

    return StatsResponse(
        short_code=link["short_code"],
        original_url=link["original_url"],
        created_at=link["created_at"],
        expires_at=link["expires_at"],
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

    # analytics удалена каскадом; инвалидируем кэш
    await cache.delete(url_cache_key(short_code))
    return DeleteResponse(short_code=short_code, deleted=True)


# ------------------------------------------------- Фоновая чистка просроченных
async def cleanup_expired() -> int:
    """Удалить просроченные ссылки (analytics уйдёт каскадом).

    Возвращает число удалённых ссылок; ошибки БД не роняют цикл чистки.
    """
    try:
        return await db.execute(
            "DELETE FROM links WHERE expires_at < ?",
            (format_ts(utc_now()),),
        )
    except sqlite3.Error:
        return 0
