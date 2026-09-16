# AGENTS.md

Express 5 + EJS URL shortener with per-link password protection. All UI and error strings are Russian — keep them that way (see `views/`, `server.js`).

## Runtime requirement
Storage uses the built-in `node:sqlite` module (`DatabaseSync`, synchronous API in `src/db.js`). **Node >= 22.5 is required**; older versions fail on import, not with a clear error.

## Commands
- `npm install` → `npm start` (or `npm run dev` = `node --watch` for auto-restart).
- Port via `PORT` env; default 3000.
- No tests, linter, or typecheck exist. Verify manually against `http://localhost:3000` (create link, follow it).

## Architecture & gotchas
- `server.js` holds all routes. `src/db.js` is a single `DatabaseSync` singleton that mkdirs `.data/` and creates `links.db` on startup (`.data/` is gitignored). Prepared statements are exported and reused.
- `src/crypto.js` stores passwords as `scrypt$salt$hash`. `src/urlcode.js` validates custom codes with `^[a-zA-Z0-9_-]{3,32}$`.
- Business rules to preserve: reserved codes are `favicon.ico`, `robots.txt`, `health`; only `http:`/`https:` targets are allowed; auth'd links show an unlock page before redirecting.
- The retro-90s neon styling in `views/*.ejs` is intentional theme, not accidental — don't "modernize" or simplify it.