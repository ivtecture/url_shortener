"""Генерация коротких кодов: base62 + криптографический генератор.

- длина 7 символов (62^7 ≈ 3.5 трлн комбинаций);
- `secrets.choice` — нельзя угадать соседние коды;
- коллизии ловит UNIQUE-индекс SQLite (см. links.py).
"""

import secrets

ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

SHORT_CODE_LENGTH = 7
MAX_INSERT_ATTEMPTS = 5


def generate_short_code(length: int = SHORT_CODE_LENGTH) -> str:
    """Случайный код заданной длины из алфавита base62."""
    return "".join(secrets.choice(ALPHABET) for _ in range(length))


def is_valid_short_code(code: str) -> bool:
    """Ровно 7 символов из base62."""
    return (
        len(code) == SHORT_CODE_LENGTH
        and all(char in ALPHABET for char in code)
    )
