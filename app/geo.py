"""Геолокация IP: приватный -> 'local', публичный -> ip-api.com + кэш 24 ч.

Fallback: Redis geo:{ip} -> ip-api.com -> 'unknown'. Сбой не роняет переход.
"""

import ipaddress

import httpx

from app.cache import cache
from app.config import settings


def extract_client_ip(forwarded_for: str | None, real_ip: str | None,
                      direct_ip: str | None) -> str:
    """Первый адрес из X-Forwarded-For, затем X-Real-IP, затем socket."""
    if forwarded_for:
        first = forwarded_for.split(",")[0].strip()
        if first:
            return first
    if real_ip:
        return real_ip.strip()
    return direct_ip or "0.0.0.0"


def is_private_ip(ip: str) -> bool:
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

    return "unknown"


if "{ip}" not in settings.geo_api_url:
    raise RuntimeError("GEO_API_URL должен содержать плейсхолдер {ip}")
