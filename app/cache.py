"""Redis-кэш с graceful-деградацией: Redis — кэш, а не source of truth.

temp-links: TTL ключей url:{code} не может превышать срок жизни самой ссылки,
поэтому setex вызывается с min(cache_ttl, до expires_at) — см. links.py.
"""

import redis.asyncio as aioredis

from app.config import settings


class Cache:
    def __init__(self, url: str) -> None:
        self._redis = aioredis.from_url(
            url,
            decode_responses=True,
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
        try:
            await self._redis.setex(key, ttl_seconds, value)
        except aioredis.RedisError:
            pass

    async def delete(self, key: str) -> None:
        try:
            await self._redis.delete(key)
        except aioredis.RedisError:
            pass

    async def ping(self) -> bool:
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
