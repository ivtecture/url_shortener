"""Подключение к SQLite через aiosqlite + создание схемы.

Схема и PRAGMA — docs/ARCHITECTURE.md, раздел 3:
- WAL: читатели не блокируют писателя;
- busy_timeout 5000: мягкое ожидание блокировки вместо мгновенной ошибки;
- foreign_keys ON: каскадное удаление analytics при удалении links;
- synchronous NORMAL: разумный баланс скорость/надёжность для WAL.

isolation_level=None (autocommit): каждая инструкция завершается сразу,
ошибка INSERT не оставляет висящей транзакции — перегенерация кода
при коллизии выполняется без ручного ROLLBACK.
"""

import aiosqlite

from app.config import settings

SCHEMA_SQL = """
CREATE TABLE IF NOT EXISTS links (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    short_code   TEXT    NOT NULL UNIQUE,
    original_url TEXT    NOT NULL,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- unique-ограничение уже создаёт индекс; явное имя оставлено для читаемости миграций
CREATE UNIQUE INDEX IF NOT EXISTS idx_links_short_code ON links(short_code);

CREATE TABLE IF NOT EXISTS analytics (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    link_id    INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    clicked_at TEXT    NOT NULL DEFAULT (datetime('now')),
    ip_address TEXT,
    country    TEXT
);

CREATE INDEX IF NOT EXISTS idx_analytics_link_id ON analytics(link_id);
CREATE INDEX IF NOT EXISTS idx_analytics_link_clicked ON analytics(link_id, clicked_at);
"""


class Database:
    """Единственное асинхронное соединение с SQLite (синглтон на процесс)."""

    def __init__(self, path: str) -> None:
        self._path = path
        self._conn: aiosqlite.Connection | None = None

    @property
    def conn(self) -> aiosqlite.Connection:
        if self._conn is None:
            raise RuntimeError("Соединение с БД не открыто: вызовите connect() в lifespan")
        return self._conn

    async def connect(self) -> None:
        self._conn = await aiosqlite.connect(
            self._path,
            isolation_level=None,  # autocommit
            check_same_thread=False,
        )
        self._conn.row_factory = aiosqlite.Row
        await self._conn.execute("PRAGMA journal_mode = WAL")
        await self._conn.execute("PRAGMA busy_timeout = 5000")
        await self._conn.execute("PRAGMA foreign_keys = ON")
        await self._conn.execute("PRAGMA synchronous = NORMAL")

    async def init_schema(self) -> None:
        """Создать таблицы/индексы, если их ещё нет (идемпотентно)."""
        await self._conn.executescript(SCHEMA_SQL)

    async def close(self) -> None:
        if self._conn is not None:
            await self._conn.close()
            self._conn = None

    # -- Утилиты для роутеров ------------------------------------------

    async def fetch_one(self, query: str, params: tuple = ()) -> aiosqlite.Row | None:
        async with self.conn.execute(query, params) as cursor:
            return await cursor.fetchone()

    async def fetch_all(self, query: str, params: tuple = ()) -> list[aiosqlite.Row]:
        async with self.conn.execute(query, params) as cursor:
            return list(await cursor.fetchall())

    async def execute(self, query: str, params: tuple = ()) -> int:
        """Выполнить statement, вернуть rowcount (для DELETE это число удалённых строк)."""
        cursor = await self.conn.execute(query, params)
        rowcount = cursor.rowcount
        await cursor.close()
        return rowcount

    async def ping(self) -> bool:
        """Проверка живости соединения для /api/v1/health."""
        try:
            await self.conn.execute("SELECT 1")
            return True
        except aiosqlite.Error:
            return False


db = Database(settings.database_path)
