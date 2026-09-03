# URL shortener

## Требования: 
1 Создание короткой ссылки по длинному URL 
2 Редирект по короткой ссылке на оригинальный URL 
3 Сбор аналитики: количество переходов, геолокация
4 Без регистрации

## Интерфейс
Стиль 2000. Статистика урла в модалке. Добавить мем про программирование. Приглушенные, холодные тона. Вводные строчки с офигительной анимацией.

## Технологический стек
* Alpine Linux
* Redis
* SQLite
* Python
* Очередь amqp нужна???
* reverse proxy нужны???
* Посоветуй по фронтенду???
* Посоветуй фреймворки бэкенда???

## API endpoints
Проектирование API  
Endpoint                           Метод    Описание 
/api/v1/links                      POST Создать короткую ссылку     
/{short_code}                      GET  Редирект на оригинальный URL
/api/v1/links/{short_code}/stats   GET  Получить статистику  
/api/v1/links/delete/{short_code}         DELETE Удалить ссылку 

## Схема базы данных (примерная, адаптируй, упрости, дополни под sqlite)

CREATE TABLE links ( 
    id BIGSERIAL PRIMARY KEY, 
    short_code VARCHAR(7) UNIQUE NOT NULL, 
    original_url TEXT NOT NULL, 
    created_at TIMESTAMP DEFAULT NOW(), 
    expires_at TIMESTAMP 
); 

CREATE TABLE analytics ( 
    id BIGSERIAL PRIMARY KEY, 
    link_id BIGINT REFERENCES links(id), 
    clicked_at TIMESTAMP DEFAULT NOW(), 
    ip_address INET, 
    country VARCHAR(2) 
); 
CREATE INDEX idx_links_short_code ON links(short_code);

## НЕ ЗАТРАГИВАТЬ
2. Сервис сокращения URL с генерацией QR-кодов - Бондаренко
3. Временные короткие ссылки - Лукина
5. Сервис сокращения URL с защитой паролем - Казаченко
9. Сервис с ограничением количества переходов - Гаранин
10. Сервис с монетизацией (реклама) - Кочетков