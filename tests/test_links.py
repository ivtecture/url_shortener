"""Автотесты лимита переходов (вариант 9) и кэша редиректа.

Закрывают замечания ревью к ветке Limits:
- лимит: создание, остаток, исчерпание, 410 Gone (JSON и HTML), удаление ссылки;
- гонка на границе лимита: параллельные переходы — ровно max_clicks × 302;
- кэш: прогрев, попадание без похода в БД, старый формат значения, мусор,
  инвалидация при исчерпании и при удалении ссылки.

Redis в тестах не используется (см. conftest.py) — кэш подменён in-memory.
"""

import asyncio

from httpx import AsyncClient

from app.config import settings
from app.database import db

TEST_URL = "https://example.com/very/long/path?query=42"


async def create_link(client: AsyncClient, url: str = TEST_URL, **extra) -> dict:
    payload = {"original_url": url, **extra}
    res = await client.post("/api/v1/links", json=payload)
    assert res.status_code == 201, res.text
    return res.json()


async def get_link_id(short_code: str) -> int:
    row = await db.fetch_one("SELECT id FROM links WHERE short_code = ?", (short_code,))
    assert row is not None
    return row["id"]


# ------------------------------------------------------------------ создание

async def test_create_link_without_limit(client):
    data = await create_link(client)
    assert data["max_clicks"] is None
    assert data["clicks_left"] is None
    assert data["short_url"] == f"{settings.base_url}/{data['short_code']}"


async def test_create_link_with_limit(client):
    data = await create_link(client, max_clicks=3)
    assert data["max_clicks"] == 3
    assert data["clicks_left"] == 3


async def test_max_clicks_validation(client):
    for bad in (0, -1, 1_000_001, "abc", 2.5):
        res = await client.post(
            "/api/v1/links",
            json={"original_url": "https://example.com", "max_clicks": bad},
        )
        assert res.status_code == 422, f"max_clicks={bad!r}: {res.text}"


async def test_non_http_scheme_rejected(client):
    res = await client.post("/api/v1/links", json={"original_url": "ftp://example.com/f"})
    assert res.status_code == 400


# ------------------------------------------------------------ редирект, лимит

async def test_redirect_unlimited_link(client):
    data = await create_link(client)
    for _ in range(3):
        res = await client.get(f"/{data['short_code']}", follow_redirects=False)
        assert res.status_code == 302
        assert res.headers["location"] == TEST_URL


async def test_limit_exhaustion_lifecycle(client):
    data = await create_link(client, max_clicks=2)
    code = data["short_code"]

    first = await client.get(f"/{code}", follow_redirects=False)
    second = await client.get(f"/{code}", follow_redirects=False)
    assert (first.status_code, second.status_code) == (302, 302)

    stats = (await client.get(f"/api/v1/links/{code}/stats")).json()
    assert stats["clicks"] == 2
    assert stats["clicks_left"] == 0

    third = await client.get(f"/{code}", follow_redirects=False)
    assert third.status_code == 410
    assert third.json()["detail"] == "Лимит переходов исчерпан (2 из 2), ссылка удалена"

    # ссылка удалена: и редирект, и статистика — обычный 404
    assert (await client.get(f"/{code}", follow_redirects=False)).status_code == 404
    assert (await client.get(f"/api/v1/links/{code}/stats")).status_code == 404


async def test_limit_410_html_page_for_browser(client):
    data = await create_link(client, max_clicks=1)
    code = data["short_code"]
    await client.get(f"/{code}", follow_redirects=False)

    res = await client.get(
        f"/{code}", follow_redirects=False, headers={"accept": "text/html"}
    )
    assert res.status_code == 410
    assert "text/html" in res.headers["content-type"]
    assert "410" in res.text
    assert code in res.text


async def test_stats_clicks_left_decreases(client):
    data = await create_link(client, max_clicks=5)
    code = data["short_code"]
    await client.get(f"/{code}", follow_redirects=False)
    await client.get(f"/{code}", follow_redirects=False)

    stats = (await client.get(f"/api/v1/links/{code}/stats")).json()
    assert stats["clicks"] == 2
    assert stats["clicks_left"] == 3
    assert stats["countries"] == [{"country": "local", "count": 2}]


async def test_parallel_requests_respect_limit(client):
    """Гонка на границе лимита: ровно max_clicks запросов получают 302,
    остальные — 410 (атомарный UPDATE ... WHERE clicks_left > 0)."""
    data = await create_link(client, max_clicks=3)
    code = data["short_code"]

    responses = await asyncio.gather(
        *(client.get(f"/{code}", follow_redirects=False) for _ in range(10))
    )
    statuses = sorted(res.status_code for res in responses)
    assert statuses == [302] * 3 + [410] * 7

    # ссылка удалена первым же исчерпавшимся запросом -> дальше обычный 404
    assert (await client.get(f"/{code}", follow_redirects=False)).status_code == 404

    # аналитика не превышает лимит (часть фоновых INSERT может не пройти
    # после удаления ссылки — каскад; это задокументированный компромисс)
    row = await db.fetch_one("SELECT COUNT(*) AS cnt FROM analytics")
    assert row["cnt"] <= 3


# ----------------------------------------------------------------------- кэш

async def test_cache_warm_and_hit_skips_db(client, fake_cache, monkeypatch):
    data = await create_link(client)
    code = data["short_code"]
    link_id = await get_link_id(code)

    first = await client.get(f"/{code}", follow_redirects=False)
    assert first.status_code == 302
    # прогрев: новый составной формат "{link_id}:{max_clicks|0}:{url}"
    assert fake_cache.store[f"url:{code}"] == f"{link_id}:0:{TEST_URL}"

    # повторный переход по бессрочной ссылке при кэш-хите не трогает БД
    calls = {"fetch_one": 0}
    original_fetch_one = db.fetch_one

    async def counting_fetch_one(query, params=()):
        calls["fetch_one"] += 1
        return await original_fetch_one(query, params)

    monkeypatch.setattr(db, "fetch_one", counting_fetch_one)

    second = await client.get(f"/{code}", follow_redirects=False)
    assert second.status_code == 302
    assert second.headers["location"] == TEST_URL
    assert calls["fetch_one"] == 0


async def test_cache_old_format_treated_as_miss(client, fake_cache):
    """Старый формат '{link_id}:{url}' (URL в середине) — распознаётся как
    промах через isdigit()-проверку, ссылка отдаётся из БД, кэш перегревается."""
    data = await create_link(client)
    code = data["short_code"]
    link_id = await get_link_id(code)

    fake_cache.store[f"url:{code}"] = f"{link_id}:{TEST_URL}"

    res = await client.get(f"/{code}", follow_redirects=False)
    assert res.status_code == 302
    assert res.headers["location"] == TEST_URL
    assert fake_cache.store[f"url:{code}"] == f"{link_id}:0:{TEST_URL}"


async def test_cache_garbage_treated_as_miss(client, fake_cache):
    data = await create_link(client)
    code = data["short_code"]

    fake_cache.store[f"url:{code}"] = "мусор-без-формата"

    res = await client.get(f"/{code}", follow_redirects=False)
    assert res.status_code == 302
    assert res.headers["location"] == TEST_URL


async def test_cache_invalidated_on_exhaustion(client, fake_cache):
    data = await create_link(client, max_clicks=1)
    code = data["short_code"]

    await client.get(f"/{code}", follow_redirects=False)
    assert f"url:{code}" in fake_cache.store

    assert (await client.get(f"/{code}", follow_redirects=False)).status_code == 410
    assert f"url:{code}" not in fake_cache.store


async def test_cache_invalidated_on_delete(client, fake_cache):
    data = await create_link(client)
    code = data["short_code"]

    await client.get(f"/{code}", follow_redirects=False)
    assert f"url:{code}" in fake_cache.store

    res = await client.delete(f"/api/v1/links/delete/{code}")
    assert res.status_code == 200
    assert res.json() == {"short_code": code, "deleted": True}
    assert f"url:{code}" not in fake_cache.store
    assert (await client.get(f"/{code}", follow_redirects=False)).status_code == 404


# ------------------------------------------------------------------ удаление

async def test_delete_link_lifecycle(client):
    data = await create_link(client, max_clicks=7)
    code = data["short_code"]

    res = await client.delete(f"/api/v1/links/delete/{code}")
    assert res.status_code == 200

    assert (await client.delete(f"/api/v1/links/delete/{code}")).status_code == 404
    assert (await client.get(f"/{code}", follow_redirects=False)).status_code == 404

