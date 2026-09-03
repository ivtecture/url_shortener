"""Генерация коротких кодов: base62, криптографический генератор.

Алгоритм — docs/ARCHITECTURE.md, раздел 5:
- длина 7 символов (62^7 ≈ 3.5 триллиона комбинаций);
- `secrets.choice` — непредсказуемость (нельзя угадать соседние коды);
- коллизии ловит UNIQUE-индекс SQLite при INSERT (см. app/links.py),
  здесь генерируем только «кандидата».
"""

import secrets

# base62: 0-9 (10) + a-z (26) + A-Z (26) = 62 символа
ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

SHORT_CODE_LENGTH = 7

# Максимальное число попыток вставки при коллизии (docs/ARCHITECTURE.md, раздел 5)
MAX_INSERT_ATTEMPTS = 5


def generate_short_code(length: int = SHORT_CODE_LENGTH) -> str:
    """Вернуть случайный код заданной длины (по умолчанию 7 символов base62)."""
    return "".join(secrets.choice(ALPHABET) for _ in range(length))


def is_valid_short_code(code: str) -> bool:
    """Проверка формата: ровно `length` символов из алфавита base62."""
    return (
        len(code) == SHORT_CODE_LENGTH
        and all(char in ALPHABET for char in code)
    )
