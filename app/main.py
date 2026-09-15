"""Точка входа FastAPI: lifespan (БД + кэш + фоновая чистка), роутеры, healthcheck.

Новое для ветки temp-links: фоновая задача cleanup_expired() удаляет
просроченные ссылки каждые CLEANUP_INTERVAL_SECONDS секунд.
Запуск (в контейнере): uvicorn app.main:app --host 0.0.0.0 --port 8000
"""

import asyncio
import contextlib
from contextlib import asynccontextmanager
from collections.abc import AsyncIterator

from fastapi import FastAPI

from app import __version__
from app.cache import cache
from app.config import settings
from app.database import db
from app.links import cleanup_expired, router as links_router
from app.models import HealthResponse


async def _cleanup_loop() -> None:
    """Бесконечный цикл чистки просроченных ссылок (гасится при остановке)."""
    while True:
        await asyncio.sleep(settings.cleanup_interval_seconds)
        with contextlib.suppress(Exception):
            await cleanup_expired()


@asynccontextmanager
async def lifespan(_: FastAPI) -> AsyncIterator[None]:
    """Открыть соединения, создать таблицы, запустить чистку; закрыть при остановке."""
    await db.connect()
    await db.init_schema()
    task = asyncio.create_task(_cleanup_loop())
    yield
    task.cancel()
    with contextlib.suppress(asyncio.CancelledError):
        await task
    await cache.close()
    await db.close()


app = FastAPI(
    title="Temporary Short Links",
    description="Анонимный сервис временных коротких ссылок: TTL, редирект, статистика переходов с геолокацией",
    version=__version__,
    lifespan=lifespan,
)

app.include_router(links_router)


@app.get(
    "/api/v1/health",
    response_model=HealthResponse,
    tags=["service"],
    summary="Healthcheck (Docker + nginx)",
)
async def health() -> HealthResponse:
    """Процесс жив; флаги db/redis отражают фактическую доступность зависимостей."""
    return HealthResponse(
        status="ok",
        db=await db.ping(),
        redis=await cache.ping(),
    )
