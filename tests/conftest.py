"""Общие фикстуры автотестов.

Redis в тестах не нужен: singleton-кэш подменяется на in-memory FakeCache
с тем же асинхронным интерфейсом (get/setex/delete) — поведение кэша,
включая значения старого формата, проверяется детерминированно.

SQLite: singleton db привязывается к пути из env при импорте app.database,
поэтому DATABASE_PATH выставляем во временный каталог ДО первого импорта
app.*. Файл БД один на сессию, между тестами таблицы чистятся.
"""

import os
import tempfile

# --- env ДО импорта app.* (pydantic-settings читает env при создании Settings) ---
_TEST_DIR = tempfile.mkdtemp(prefix="urlshort-tests-")
os.environ["DATABASE_PATH"] = os.path.join(_TEST_DIR, "test.db")
os.environ["BASE_URL"] = "http://testserver"

import pytest  # noqa: E402
from httpx import ASGITransport, AsyncClient  # noqa: E402


class FakeCache:
    """In-memory замена Cache: тот же интерфейс, без реального Redis."""

    def __init__(self) -> None:
        self.store: dict[str, str] = {}

    async def get(self, key: str) -> str | None:
        return self.store.get(key)

    async def setex(self, key: str, ttl_seconds: int, value: str) -> None:
        self.store[key] = value

    async def delete(self, key: str) -> None:
        self.store.pop(key, None)


@pytest.fixture
def fake_cache(monkeypatch):
    """Подменяет методы singleton-кэша на in-memory реализацию.

    app.links / app.geo импортируют тот же объект cache, поэтому патч
    методов экземпляра действует на весь код приложения.
    """
    from app.cache import cache

    fake = FakeCache()
    monkeypatch.setattr(cache, "get", fake.get)
    monkeypatch.setattr(cache, "setex", fake.setex)
    monkeypatch.setattr(cache, "delete", fake.delete)
    return fake


@pytest.fixture
async def client(fake_cache):
    """AsyncClient поверх ASGI-приложения с запущенным lifespan.

    lifespan открывает соединение SQLite и создаёт схему; между тестами
    таблицы чистятся. Фоновые задачи (record_click) успевают выполниться
    до возврата ответа: ASGITransport дожидается завершения ASGI-вызова.
    """
    from app import main
    from app.database import db

    async with main.app.router.lifespan_context(main.app):
        await db.execute("DELETE FROM analytics")
        await db.execute("DELETE FROM links")
        fake_cache.store.clear()
        async with AsyncClient(
            transport=ASGITransport(app=main.app),
            base_url="http://testserver",
        ) as async_client:
            yield async_client
