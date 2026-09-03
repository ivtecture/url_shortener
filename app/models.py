"""Pydantic-схемы запросов и ответов API (docs/ARCHITECTURE.md, раздел 4).

Правила валидации POST /api/v1/links:
- пустое поле / не-JSON / не-URL  -> 422 (pydantic автоматически);
- схема не http/https или длина > 2048 -> 400 (проверяется в роутере,
  чтобы вернуть текст ошибки из спецификации).
"""

from urllib.parse import urlparse

from pydantic import BaseModel, Field, field_validator


class LinkCreate(BaseModel):
    """Тело POST /api/v1/links."""

    original_url: str = Field(..., min_length=1, description="Длинный URL для сокращения")

    @field_validator("original_url")
    @classmethod
    def must_be_absolute_url(cls, value: str) -> str:
        """Отсекаем строки, которые вообще не являются абсолютными URL -> 422.

        Проверку схемы (http/https) и длины сознательно делаем в роутере:
        для них спецификация требует 400 с конкретным текстом ошибки.
        """
        url = value.strip()
        parsed = urlparse(url)
        if not parsed.scheme or not parsed.netloc:
            raise ValueError("Input should be a valid URL")
        return url


class LinkResponse(BaseModel):
    """Ответ 201 на создание ссылки."""

    short_code: str
    short_url: str
    original_url: str
    created_at: str


class CountryClicks(BaseModel):
    """Строка группировки «страна — число переходов» в статистике."""

    country: str
    count: int


class StatsResponse(BaseModel):
    """Ответ 200 GET /api/v1/links/{short_code}/stats."""

    short_code: str
    original_url: str
    created_at: str
    clicks: int
    countries: list[CountryClicks]


class DeleteResponse(BaseModel):
    """Ответ на DELETE /api/v1/links/delete/{short_code}."""

    short_code: str
    deleted: bool = True


class HealthResponse(BaseModel):
    """Ответ GET /api/v1/health (флаги — по факту доступности зависимостей)."""

    status: str
    db: bool
    redis: bool
