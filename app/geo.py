"""Геолокация IP-адреса: приватный -> 'local', публичный -> ip-api.com + кэш.

Fallback-цепочка (docs/ARCHITECTURE.md, раздел 6):
    geo:{ip} в Redis  ->  http://ip-api.com/json/{ip}?fields=status,countryCode  ->  'unknown'

- таймаут внешнего запроса GEO_TIMEOUT_SECONDS (2 с);
- результат кэшируется в Redis на 24 ч (лимит ip-api.com — 45 запросов/мин);
- при любом сбое страна 'unknown': переход фиксируется всегда, кэш не отравляем.
"""

import ipaddress

import httpx

from app.cache import cache
from app.config import settings


def extract_client_ip(forwarded_for: str | None, real_ip: str | None,
                      direct_ip: str | None) -> str:
    """IP клиента: первый адрес из X-Forwarded-For, затем X-Real-IP, затем socket.

    За nginx заголовок X-Forwarded-For перезаписывается ($proxy_add_x_forwarded_for),
    так что первый элемент — реальный адрес клиента.
    """
    if forwarded_for:
        # "203.0.113.7, 10.0.0.1, ..." -> первый адрес
        first = forwarded_for.split(",")[0].strip()
        if first:
            return first
    if real_ip:
        return real_ip.strip()
    return direct_ip or "0.0.0.0"


def is_private_ip(ip: str) -> bool:
    """Приватные/loopback/link-local/ULA-адреса не геолокируем вовсе.

    Покрывает 10/8, 172.16/12, 192.168/16, 127.*, ::1, fc00::/7 и пр.
    Битые строки трактуем как приватные — внешний вызов не делаем.
    """
    try:
        addr = ipaddress.ip_address(ip)
    except ValueError:
        return True
    return addr.is_private or addr.is_loopback or addr.is_link_local or addr.is_reserved


async def resolve_country(ip: str) -> str:
    """ISO 3166-1 alpha-2 | 'local' | 'unknown'."""
    if is_private_ip(ip):
        return "local"

    cached = await cache.get(f"geo:{ip}")
    if cached:
        return cached

    try:
        async with httpx.AsyncClient(timeout=settings.geo_timeout_seconds) as client:
            response = await client.get(settings.geo_api_url.format(ip=ip))
            data = response.json()
        if data.get("status") == "success" and data.get("countryCode"):
            country = str(data["countryCode"])
            await cache.setex(f"geo:{ip}", settings.geo_cache_ttl_seconds, country)
            return country
    except (httpx.HTTPError, ValueError):
        pass

    # Сбой/таймаут/лимит: страна уточнится при следующих переходах с этого IP
    return "unknown"


# Проверка шаблона GEO_API_URL при импорте — ранний фейл при кривой конфигурации
if "{ip}" not in settings.geo_api_url:
    raise RuntimeError("GEO_API_URL должен содержать плейсхолдер {ip}")
