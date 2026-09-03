"""Обёртка над redis.asyncio.

Redis — кэш, а не source of truth (docs/ARCHITECTURE.md, риск №8):
при недоступности сервера все операции молча деградируют
(GET -> None = промах, SET -> пропуск), сервис продолжает работать на SQLite.
"""

import redis.asyncio as aioredis

from app.config import settings


class Cache:
    """Тонкая обёртка с graceful-деградацией при падении Redis."""

    def __init__(self, url: str) -> None:
        self._redis = aioredis.from_url(
            url,
            decode_responses=True,  # str вместо bytes
            socket_connect_timeout=2,
            socket_timeout=2,
        )

    async def get(self, key: str) -> str | None:
        """Значение по ключу; None — промах ИЛИ недоступный Redis."""
        try:
            return await self._redis.get(key)
        except aioredis.RedisError:
            return None

    async def setex(self, key: str, ttl_seconds: int, value: str) -> None:
        """Записать значение с TTL. Ошибки (в т.ч. READONLY) не роняют запрос."""
        try:
            await self._redis.setex(key, ttl_seconds, value)
        except aioredis.RedisError:
            pass

    async def delete(self, key: str) -> None:
        """Инвалидация ключа (после удаления ссылки)."""
        try:
            await self._redis.delete(key)
        except aioredis.RedisError:
            pass

    async def ping(self) -> bool:
        """Проверка живости для /api/v1/health."""
        try:
            return bool(await self._redis.ping())
        except aioredis.RedisError:
            return False

    async def close(self) -> None:
        try:
            await self._redis.aclose()
        except aioredis.RedisError:
            pass


cache = Cache(settings.redis_url)
