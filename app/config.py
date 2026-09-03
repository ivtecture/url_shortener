"""Настройки приложения из переменных окружения (pydantic-settings).

Имена и значения по умолчанию — из docs/ARCHITECTURE.md, раздел 7.3.
"""

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """Все параметры конфигурируются через env (или файл .env)."""

    # Путь к файлу SQLite (на docker-томе sqlite-data)
    database_path: str = "/data/urlshortener.db"

    # Подключение к Redis
    redis_url: str = "redis://redis:6379/0"

    # Шаблон гео-запроса: {ip} подставляется автоматически
    geo_api_url: str = "http://ip-api.com/json/{ip}?fields=status,countryCode"

    # Таймаут внешнего гео-запроса, сек
    geo_timeout_seconds: float = 2.0

    # TTL кэша редиректов url:{short_code}, сек
    cache_ttl_seconds: int = 3600

    # TTL кэша геолокации geo:{ip}, сек (24 часа)
    geo_cache_ttl_seconds: int = 86400

    # База для сборки short_url в ответах API
    base_url: str = "http://localhost"

    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8", extra="ignore")


settings = Settings()
