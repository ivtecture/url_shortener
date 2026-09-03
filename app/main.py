"""Точка входа FastAPI: lifespan (БД + кэш), роутеры, healthcheck.

Запуск (в контейнере): uvicorn app.main:app --host 0.0.0.0 --port 8000
"""

from contextlib import asynccontextmanager
from collections.abc import AsyncIterator

from fastapi import FastAPI

from app import __version__
from app.cache import cache
from app.database import db
from app.links import router as links_router
from app.models import HealthResponse


@asynccontextmanager
async def lifespan(_: FastAPI) -> AsyncIterator[None]:
    """Открыть соединения и создать таблицы при старте; закрыть при остановке."""
    await db.connect()
    await db.init_schema()
    yield
    await cache.close()
    await db.close()


app = FastAPI(
    title="URL Shortener",
    description="Анонимный сокращатель URL с аналитикой переходов и геолокацией",
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
