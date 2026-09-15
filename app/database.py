"""Обёртка над aiosqlite: подключение, PRAGMA, схема с полем expires_at.

Схема (ветка temp-links):
- links.expires_at  — «YYYY-MM-DD HH:MM:SS» UTC, строка; сравнение строк
  корректно, т.к. формат фиксированный (datetime('now') даёт тот же формат);
- индекс idx_links_expires — по нему фоновая чистка находит просроченные;
- analytics.link_id — ON DELETE CASCADE: удаление ссылки сносит её статистику.
"""

import aiosqlite

from app.config import settings

SCHEMA = """
CREATE TABLE IF NOT EXISTS links (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    short_code   TEXT    NOT NULL UNIQUE,
    original_url TEXT    NOT NULL,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    expires_at   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_links_short_code ON links(short_code);
CREATE INDEX IF NOT EXISTS idx_links_expires    ON links(expires_at);

CREATE TABLE IF NOT EXISTS analytics (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    link_id    INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    clicked_at TEXT    NOT NULL DEFAULT (datetime('now')),
    ip_address TEXT,
    country    VARCHAR(2)
);
CREATE INDEX IF NOT EXISTS idx_analytics_link_id ON analytics(link_id);
"""


class Database:
    """Тонкая обёртка над aiosqlite: WAL + busy_timeout + foreign_keys."""

    def __init__(self, path: str) -> None:
        self._path = path
        self._conn: aiosqlite.Connection | None = None

    async def connect(self) -> None:
        self._conn = await aiosqlite.connect(self._path)
        self._conn.row_factory = aiosqlite.Row
        await self._conn.execute("PRAGMA journal_mode=WAL")
        await self._conn.execute("PRAGMA busy_timeout=5000")
        await self._conn.execute("PRAGMA foreign_keys=ON")
        await self._conn.commit()

    async def init_schema(self) -> None:
        await self._conn.executescript(SCHEMA)
        await self._conn.commit()

    async def execute(self, sql: str, params: tuple = ()) -> int:
        """Выполнить запись; вернуть число затронутых строк."""
        cursor = await self._conn.execute(sql, params)
        await self._conn.commit()
        return cursor.rowcount

    async def fetch_one(self, sql: str, params: tuple = ()) -> aiosqlite.Row | None:
        cursor = await self._conn.execute(sql, params)
        return await cursor.fetchone()

    async def fetch_all(self, sql: str, params: tuple = ()) -> list[aiosqlite.Row]:
        cursor = await self._conn.execute(sql, params)
        return await cursor.fetchall()

    async def ping(self) -> bool:
        try:
            await self._conn.execute("SELECT 1")
            return True
        except aiosqlite.Error:
            return False

    async def close(self) -> None:
        if self._conn:
            await self._conn.close()
            self._conn = None


db = Database(settings.database_path)
