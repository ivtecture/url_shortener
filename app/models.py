"""Pydantic-схемы запросов и ответов API.

Отличия от базовой версии (ветка temp-links):
- LinkCreate принимает ttl_seconds (опционально, 60..604800);
- LinkResponse / StatsResponse содержат expires_at.
"""

from pydantic import BaseModel, Field, HttpUrl


class LinkCreate(BaseModel):
    """Тело POST /api/v1/links. ttl_seconds не указан -> дефолт из настроек."""
    original_url: HttpUrl
    ttl_seconds: int | None = Field(
        default=None,
        ge=60,
        le=604800,
        description="Срок жизни ссылки в секундах (60..604800)",
    )


class LinkResponse(BaseModel):
    short_code: str
    short_url: str
    original_url: str
    expires_at: str  # "YYYY-MM-DD HH:MM:SS" UTC


class CountryClicks(BaseModel):
    country: str
    count: int


class StatsResponse(BaseModel):
    short_code: str
    original_url: str
    created_at: str
    expires_at: str
    clicks: int
    countries: list[CountryClicks]


class DeleteResponse(BaseModel):
    short_code: str
    deleted: bool


class HealthResponse(BaseModel):
    status: str
    db: bool
    redis: bool
