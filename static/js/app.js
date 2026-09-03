/* ============================================================
   ShortURL 2000 — ванильный JS без зависимостей.
   1) печатающийся placeholder + мигающая каретка
   2) создание ссылки  -> POST /api/v1/links
   3) копирование      -> Clipboard API (+execCommand fallback)
   4) модалка статистики -> GET /api/v1/links/{code}/stats
   5) удаление         -> DELETE /api/v1/links/delete/{code}
   6) ретро-счётчик посещений (localStorage)
   ============================================================ */
"use strict";

/* ---------- Хелперы ---------- */

const $ = (id) => document.getElementById(id);

const BASE_URL = window.location.origin;

function showError(boxEl, message) {
    boxEl.textContent = message;
    boxEl.classList.remove("hidden");
}

function hideError(boxEl) {
    boxEl.classList.add("hidden");
}

/* FastAPI отдаёт detail строкой (400/404/503) или массивом объектов (422 pydantic) */
function extractErrorMessage(payload, fallback) {
    if (typeof payload === "string" && payload) return payload;
    if (Array.isArray(payload)) {
        return payload
            .map((e) => {
                const where = Array.isArray(e.loc) ? e.loc.slice(1).join(".") : "";
                return (where ? where + ": " : "") + (e.msg || "");
            })
            .join("; ");
    }
    return fallback;
}

/* ISO-код страны -> эмодзи-флаг (только для A-Z); спец-значения -> спец-эмодзи */
function countryLabel(code) {
    if (code === "local") return "\uD83D\uDCBB local";
    if (!code || code === "unknown") return "\u2753 unknown";
    if (/^[A-Za-z]{2}$/.test(code)) {
        const flag = code
            .toUpperCase()
            .split("")
            .map((ch) => String.fromCodePoint(127397 + ch.charCodeAt(0)))
            .join("");
        return flag + " " + code.toUpperCase();
    }
    return code;
}

/* ---------- 1) Печатающийся placeholder ---------- */

(function typePlaceholder() {
    const input = $("url-input");
    const caret = $("fake-caret");
    const phrases = [
        "https://very-long-address.ru/forum/thread.php?id=42&page=1337",
        "http://example.com/some/very/long/path?query=42&utm_source=mail",
        "https://help.me.please.my.url.is.too.long.ua/download?file=setup.exe",
    ];
    let phraseIdx = 0;
    let charIdx = 0;
    let deleting = false;

    function setPlaceholder(text) {
        /* рисуем «печатанку» прямо в пустом поле */
        input.setAttribute("placeholder", text);
        caret.style.display = text || document.activeElement === input ? "none" : "inline";
        if (document.activeElement === input) caret.style.display = "none";
    }

    function tick() {
        if (document.activeElement === input || input.value) {
            /* не мешаем живому пользователю */
            caret.style.display = "none";
            input.setAttribute("placeholder", "");
            window.setTimeout(tick, 900);
            return;
        }
        caret.style.display = "inline";
        const phrase = phrases[phraseIdx];
        if (!deleting) {
            charIdx++;
            setPlaceholder(phrase.slice(0, charIdx));
            if (charIdx >= phrase.length) {
                deleting = true;
                window.setTimeout(tick, 2200); /* пауза, чтобы прочитать */
                return;
            }
        } else {
            charIdx--;
            setPlaceholder(phrase.slice(0, charIdx));
            if (charIdx <= 0) {
                deleting = false;
                phraseIdx = (phraseIdx + 1) % phrases.length;
            }
        }
        window.setTimeout(tick, deleting ? 18 : 55);
    }

    window.setTimeout(tick, 600);
})();

/* ---------- 2) Создание ссылки ---------- */

const form = $("shorten-form");
const urlInput = $("url-input");
const errorBox = $("error-box");
const resultBox = $("result");
const shortLink = $("short-link");
const copyBtn = $("copy-btn");
const deleteBtn = $("delete-btn");
const statsBtn = $("stats-btn");
let currentCode = null;

form.addEventListener("submit", async (event) => {
    event.preventDefault();
    hideError(errorBox);
    resultBox.classList.add("hidden");
    currentCode = null;

    const value = urlInput.value.trim();
    if (!value) {
        showError(errorBox, "Введите URL — поле пустое!");
        return;
    }

    const btn = $("shorten-btn");
    btn.disabled = true;
    btn.textContent = "Сокращаем...";
    try {
        const res = await fetch("/api/v1/links", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ original_url: value }),
        });

        if (res.status === 201) {
            const data = await res.json(); /* { short_code, short_url, ... } */
            currentCode = data.short_code;
            shortLink.textContent = data.short_url;
            shortLink.href = data.short_url;
            resultBox.classList.remove("hidden");
            urlInput.select();
        } else {
            const err = await res.json().catch(() => ({}));
            showError(errorBox, extractErrorMessage(err.detail, "Ошибка " + res.status));
        }
    } catch (networkError) {
        showError(errorBox, "Сервер недоступен. Проверьте, что nginx поднят :)");
    } finally {
        btn.disabled = false;
        btn.innerHTML = '<span class="btn-glyph">&#9986;</span> Сократить!';
    }
});

/* ---------- 3) Копирование ---------- */

copyBtn.addEventListener("click", async () => {
    const text = shortLink.textContent;
    if (!text) return;
    try {
        await navigator.clipboard.writeText(text);
    } catch (_e) {
        /* fallback для старых браузеров / http */
        const ta = document.createElement("textarea");
        ta.value = text;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
    }
    const old = copyBtn.textContent;
    copyBtn.textContent = "\u2714 Скопировано!";
    window.setTimeout(() => (copyBtn.textContent = old), 1500);
});

/* ---------- 4) Модалка статистики ---------- */

const modal = $("stats-modal");
const modalError = $("modal-error");
const mCountries = $("m-countries");
const mClicks = $("m-clicks");
const mShort = $("m-short");
const mOriginal = $("m-original");
const mCreated = $("m-created");
const modalCodeLabel = $("modal-code-label");
let modalCode = null;

function openModal(code) {
    if (!code) return;
    modalCode = code;
    modalCodeLabel.textContent = code;
    hideError(modalError);
    mCountries.innerHTML = '<tr class="empty-row"><td colspan="3">загрузка...</td></tr>';
    mClicks.textContent = "\u2026";
    mShort.textContent = BASE_URL + "/" + code;
    mShort.href = BASE_URL + "/" + code;
    mOriginal.textContent = "";
    mCreated.textContent = "";
    modal.classList.remove("hidden");
    loadStats();
}

function closeModal() {
    modal.classList.add("hidden");
    modalCode = null;
}

async function loadStats() {
    if (!modalCode) return;
    hideError(modalError);
    try {
        const res = await fetch("/api/v1/links/" + encodeURIComponent(modalCode) + "/stats");
        if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            mCountries.innerHTML = "";
            mClicks.textContent = "0";
            showError(modalError, extractErrorMessage(err.detail, "Статистика недоступна"));
            return;
        }
        const data = await res.json();
        renderStats(data);
    } catch (_e) {
        showError(modalError, "Сервер недоступен");
    }
}

function renderStats(data) {
    mOriginal.textContent = data.original_url;
    mCreated.textContent = data.created_at;
    mClicks.textContent = data.clicks;

    if (!data.countries.length) {
        mCountries.innerHTML =
            '<tr class="empty-row"><td colspan="3">нет данных &mdash; по ссылке ещё никто не ходил</td></tr>';
        return;
    }

    const max = Math.max(...data.countries.map((c) => c.count), 1);
    mCountries.innerHTML = data.countries
        .map(
            (row) =>
                "<tr><td>" + countryLabel(row.country) + "</td><td class=\"mono\">" + row.count +
                '</td><td><span class="country-bar" style="width:' +
                Math.max((row.count / max) * 100, 3) + '%"></span></td></tr>'
        )
        .join("");
}

statsBtn.addEventListener("click", () => openModal(currentCode));
$("m-refresh").addEventListener("click", loadStats);
$("m-close").addEventListener("click", closeModal);
$("modal-close-x").addEventListener("click", closeModal);

/* сайдбар: статистика по введённому коду */
$("stats-code-btn").addEventListener("click", () => {
    const code = $("stats-code-input").value.trim();
    if (!/^[0-9a-zA-Z]{7}$/.test(code)) {
        alert("Код должен состоять ровно из 7 символов (буквы/цифры).");
        return;
    }
    openModal(code);
});

/* закрытие: фон, Esc */
modal.addEventListener("click", (event) => {
    if (event.target === modal) closeModal();
});
document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !modal.classList.contains("hidden")) closeModal();
});

/* ---------- 5) Удаление ссылки ---------- */

deleteBtn.addEventListener("click", async () => {
    if (!currentCode) return;
    if (!window.confirm("Удалить ссылку " + currentCode + " вместе со всей статистикой?")) return;

    try {
        const res = await fetch("/api/v1/links/delete/" + encodeURIComponent(currentCode), {
            method: "DELETE",
        });
        if (res.ok) {
            resultBox.classList.add("hidden");
            currentCode = null;
            alert("Ссылка удалена. Момент молчания по её переходам...");
        } else {
            const err = await res.json().catch(() => ({}));
            showError(errorBox, extractErrorMessage(err.detail, "Не удалось удалить"));
        }
    } catch (_e) {
        showError(errorBox, "Сервер недоступен");
    }
});

/* ---------- 6) Ретро-счётчик посещений ---------- */

(function visitCounter() {
    const KEY = "shorturl2000_visits";
    let visits = parseInt(window.localStorage.getItem(KEY) || "0", 10) || 0;
    visits += 1;
    window.localStorage.setItem(KEY, String(visits));
    $("visit-counter").textContent = String(visits).padStart(6, "0");
})();
