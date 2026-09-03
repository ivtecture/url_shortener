# URL Shortener — FastAPI-приложение (docs/ARCHITECTURE.md, раздел 7)
FROM python:3.12-alpine

# Не писать .pyc и не буферизовать stdout — логи видны сразу
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /srv

# Слой зависимостей отдельно от кода — кэшируется докером
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app ./app

# Non-root пользователь + каталог для SQLite-тома с правами записи
RUN addgroup -S appgroup \
    && adduser -S appuser -G appgroup \
    && mkdir -p /data \
    && chown -R appuser:appgroup /srv /data

USER appuser

EXPOSE 8000

# busybox-wget из базового образа используется healthcheck'ом compose
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]
