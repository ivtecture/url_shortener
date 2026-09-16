const path = require('node:path');
const express = require('express');
const { insertLink, getByCode } = require('./src/db');
const { hashPassword, verifyPassword } = require('./src/crypto');
const { generateCode, isValidCustomCode } = require('./src/urlcode');

const app = express();
const PORT = process.env.PORT || 3000;

const RESERVED_CODES = new Set(['favicon.ico', 'robots.txt', 'health']);

app.set('view engine', 'ejs');
app.set('views', path.join(__dirname, 'views'));
app.use(express.urlencoded({ extended: false }));

function detectBaseUrl(req) {
  return `${req.protocol}://${req.get('host')}`;
}

app.get('/', (req, res) => {
  res.render('index', { result: null, error: null, baseUrl: detectBaseUrl(req) });
});

app.post('/', (req, res) => {
  const url = String(req.body.url || '').trim();
  const password = req.body.password || '';
  let code = String(req.body.code || '').trim();

  const renderError = (error) =>
    res.status(400).render('index', { result: null, error, baseUrl: detectBaseUrl(req) });

  let parsed;
  try {
    parsed = new URL(url);
  } catch {
    return renderError('Введите корректный URL');
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return renderError('Поддерживаются только http:// и https:// ссылки');
  }

  if (code) {
    if (!isValidCustomCode(code)) {
      return renderError('Свой код должен быть 3–32 символа: буквы, цифры, "_" или "-"');
    }
    if (RESERVED_CODES.has(code)) {
      return renderError('Этот код зарезервирован, выберите другой');
    }
    if (getByCode.get(code)) {
      return renderError('Такой код уже занят, выберите другой');
    }
  } else {
    do {
      code = generateCode();
    } while (getByCode.get(code));
  }

  const passwordHash = password ? hashPassword(password) : null;
  insertLink.run(code, url, passwordHash);
  res.render('index', {
    result: `${detectBaseUrl(req)}/${code}`,
    error: null,
    baseUrl: detectBaseUrl(req),
  });
});

app.post('/:code', (req, res) => {
  const link = getByCode.get(req.params.code);
  if (!link) return res.status(404).send('Ссылка не найдена');
  if (!link.password_hash) return res.redirect(link.url);
  if (verifyPassword(req.body.password || '', link.password_hash)) {
    return res.redirect(link.url);
  }
  res.status(401).render('unlock', {
    code: link.code,
    error: 'Неверный пароль',
  });
});

app.get('/:code', (req, res) => {
  const link = getByCode.get(req.params.code);
  if (!link) return res.status(404).render('index', {
    result: null,
    error: 'Ссылка не найдена',
    baseUrl: detectBaseUrl(req),
  });
  if (link.password_hash) {
    return res.render('unlock', { code: link.code, error: null });
  }
  res.redirect(302, link.url);
});

app.listen(PORT, () => {
  console.log(`Сервис запущен: http://localhost:${PORT}`);
});