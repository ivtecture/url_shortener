"""Миграция старых БД: бэкфилл clicks_left при добавлении колонки.

Воспроизводим схему до нововведения (links без clicks_left) с накопленной
аналитикой и проверяем, что init_schema() досоздаёт колонку и заполняет
остаток как «лимит минус фактические переходы», не трогая бессрочные.
"""

import aiosqlite

from app.database import Database

OLD_SCHEMA = """
CREATE TABLE links (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    short_code   TEXT    NOT NULL UNIQUE,
    original_url TEXT    NOT NULL,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    max_clicks   INTEGER
);
CREATE TABLE analytics (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    link_id    INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    clicked_at TEXT    NOT NULL DEFAULT (datetime('now')),
    ip_address TEXT,
    country    TEXT
);
"""


async def _seed_old_db(path: str) -> None:
    conn = await aiosqlite.connect(path)
    await conn.executescript(OLD_SCHEMA)
    await conn.executescript(
        """
        INSERT INTO links (short_code, original_url, max_clicks) VALUES
            ('aB3xK9z', 'https://example.com/partial', 5),  -- 2 перехода -> остаток 3
            ('zZ9zZ9z', 'https://example.com/drained', 1),  -- 3 перехода -> остаток 0
            ('qQ1qQ1q', 'https://example.com/eternal', NULL);
        INSERT INTO analytics (link_id, ip_address, country) VALUES
            (1, '127.0.0.1', 'local'),
            (1, '127.0.0.1', 'local'),
            (2, '127.0.0.1', 'local'),
            (2, '127.0.0.1', 'local'),
            (2, '127.0.0.1', 'local');
        """
    )
    await conn.commit()
    await conn.close()


async def test_clicks_left_backfilled_for_old_limited_links(tmp_path):
    db_file = str(tmp_path / "old-format.db")
    await _seed_old_db(db_file)

    fresh = Database(db_file)
    await fresh.connect()
    try:
        await fresh.init_schema()  # ALTER TABLE + бэкфилл

        rows = await fresh.fetch_all(
            "SELECT short_code, max_clicks, clicks_left FROM links ORDER BY id"
        )
        by_code = {row["short_code"]: row for row in rows}
        assert by_code["aB3xK9z"]["clicks_left"] == 3
        assert by_code["zZ9zZ9z"]["clicks_left"] == 0      # MAX(..., 0) не уходит в минус
        assert by_code["qQ1qQ1q"]["clicks_left"] is None   # бессрочная остаётся NULL

        # миграция идемпотентна: повторный init_schema ничего не меняет
        await fresh.init_schema()
        rows2 = await fresh.fetch_all(
            "SELECT short_code, clicks_left FROM links ORDER BY id"
        )
        assert [r["clicks_left"] for r in rows2] == [3, 0, None]
    finally:
        await fresh.close()
