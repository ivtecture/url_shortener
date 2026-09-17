"""Настройки из переменных окружения (pydantic-settings).

Новое для ветки temp-links:
    DEFAULT_TTL_SECONDS       — срок жизни ссылки, если не указан (1 час);
    MIN_TTL_SECONDS           — нижняя граница TTL (60 сек);
    MAX_TTL_SECONDS           — верхняя граница TTL (7 дней);
    CLEANUP_INTERVAL_SECONDS  — период фоновой чистки просроченных (5 мин).
"""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8", extra="ignore")

    # --- инфраструктура ---
    database_path: str = "/data/urlshortener.db"
    redis_url: str = "redis://redis:6379/0"
    base_url: str = "http://localhost"

    # --- кэш ---
    cache_ttl_seconds: int = 3600        # TTL ключа url:{code} в Redis

    # --- геолокация ---
    geo_api_url: str = "http://ip-api.com/json/{ip}?fields=status,countryCode"
    geo_timeout_seconds: float = 2
    geo_cache_ttl_seconds: int = 86400   # TTL ключа geo:{ip}

    # --- временные ссылки (ветка temp-links) ---
    default_ttl_seconds: int = 3600      # 1 час, если клиент не выбрал срок
    min_ttl_seconds: int = 60            # меньше минуты ссылка не имеет смысла
    max_ttl_seconds: int = 604800        # 7 дней — потолок для «временной» ссылки
    cleanup_interval_seconds: int = 300  # чистка просроченных раз в 5 минут


settings = Settings()
