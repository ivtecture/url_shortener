const path = require('node:path');
const fs = require('node:fs');
const { DatabaseSync } = require('node:sqlite');

const DATA_DIR = path.join(__dirname, '..', '.data');
fs.mkdirSync(DATA_DIR, { recursive: true });

const db = new DatabaseSync(path.join(DATA_DIR, 'links.db'));

db.exec(`
  CREATE TABLE IF NOT EXISTS links (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    code          TEXT    NOT NULL UNIQUE,
    url           TEXT    NOT NULL,
    password_hash TEXT,
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
  )
`);

const insertLink = db.prepare(
  'INSERT INTO links (code, url, password_hash) VALUES (?, ?, ?)'
);
const getByCode = db.prepare('SELECT * FROM links WHERE code = ?');

module.exports = { db, insertLink, getByCode };