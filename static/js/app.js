/* ============================================================
   Времянка — ванильный JS без зависимостей.
   1) создание ссылки   -> POST /api/v1/links (+ ttl_seconds)
   2) копирование       -> Clipboard API (+execCommand fallback)
   3) модалка статистики -> GET /api/v1/links/{code}/stats
   4) удаление          -> DELETE /api/v1/links/delete/{code}
   5) пасхалки: 10 кликов по логотипу + код Konami
   ============================================================ */
"use strict";

/* ---------- Хелперы ---------- */

const $ = (id) => document.getElementById(id);

const errorBox = $("error-box");
const resultBox = $("result");
const form = $("shorten-form");
const urlInput = $("url-input");
const shortenBtn = $("shorten-btn");

let currentCode = null;

function showError(boxEl, message) {
    boxEl.textContent = message;
    boxEl.classList.remove("hidden");
}

function hideError(boxEl) {
    boxEl.classList.add("hidden");
}

let toastTimer = null;
function toast(message) {
    const el = $("toast");
    el.textContent = message;
    el.classList.remove("hidden");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.add("hidden"), 2600);
}

/* FastAPI отдаёт detail строкой (400/404/410/503) или массивом объектов (422 pydantic) */
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

/* ISO-код страны -> эмодзи-флаг; спец-значения -> эмодзи */
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

function selectedTtl() {
    const checked = form.querySelector('input[name="ttl"]:checked');
    return checked ? parseInt(checked.value, 10) : 3600;
}

/* ---------- 1) Создание ссылки ---------- */

form.addEventListener("submit", async (event) => {
    event.preventDefault();
    hideError(errorBox);

    const originalUrl = urlInput.value.trim();
    if (!originalUrl) {
        showError(errorBox, "Вставьте ссылку — магия требует материал.");
        return;
    }

    shortenBtn.disabled = true;
    try {
        const res = await fetch("/api/v1/links", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ original_url: originalUrl, ttl_seconds: selectedTtl() }),
        });
        const data = await res.json().catch(() => ({}));

        if (!res.ok) {
            showError(errorBox, extractErrorMessage(data.detail, "Не удалось сократить ссылку"));
            return;
        }

        currentCode = data.short_code;
        $("short-link").textContent = data.short_url;
        $("short-link").href = data.short_url;
        $("expires-at").textContent = data.expires_at;
        resultBox.classList.remove("hidden");
        toast("Готово! Ссылка живёт ограниченное время");
    } catch (_e) {
        showError(errorBox, "Сервер недоступен");
    } finally {
        shortenBtn.disabled = false;
    }
});

/* ---------- 2) Копирование ---------- */

$("copy-btn").addEventListener("click", async () => {
    const text = $("short-link").textContent;
    if (!text) return;
    try {
        await navigator.clipboard.writeText(text);
        toast("Скопировано в буфер");
    } catch (_e) {
        /* fallback для старых браузеров / http */
        const tmp = document.createElement("textarea");
        tmp.value = text;
        document.body.appendChild(tmp);
        tmp.select();
        document.execCommand("copy");
        tmp.remove();
        toast("Скопировано (fallback)");
    }
});

/* ---------- 3) Модалка статистики ---------- */

const modal = $("stats-modal");
const modalError = $("modal-error");
const mCountries = $("m-countries");
const mClicks = $("m-clicks");
let modalCode = null;

function openModal(code) {
    modalCode = code;
    $("modal-code-label").textContent = "/" + code;
    hideError(modalError);
    mClicks.textContent = "0";
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
            mCountries.innerHTML = '<tr class="empty-row"><td colspan="3">нет данных</td></tr>';
            mClicks.textContent = "0";
            showError(modalError, extractErrorMessage(err.detail, "Статистика недоступна"));
            return;
        }
        renderStats(await res.json());
    } catch (_e) {
        showError(modalError, "Сервер недоступен");
    }
}

function renderStats(data) {
    $("m-original").textContent = data.original_url;
    $("m-created").textContent = data.created_at;
    $("m-expires").textContent = data.expires_at;
    mClicks.textContent = data.clicks;

    if (!data.countries.length) {
        mCountries.innerHTML =
            '<tr class="empty-row"><td colspan="3">пока никто не переходил</td></tr>';
        return;
    }

    const max = Math.max(...data.countries.map((c) => c.count), 1);
    mCountries.innerHTML = data.countries
        .map((row) =>
            "<tr><td>" + countryLabel(row.country) + '</td><td class="mono">' + row.count +
            '</td><td><span class="country-bar" style="width:' +
            Math.max((row.count / max) * 100, 3) + '%"></span></td></tr>'
        )
        .join("");
}

$("stats-btn").addEventListener("click", () => openModal(currentCode));
$("m-refresh").addEventListener("click", loadStats);
$("m-close").addEventListener("click", closeModal);
$("modal-close-x").addEventListener("click", closeModal);

/* сайдбар: статистика по введённому коду */
$("stats-code-btn").addEventListener("click", () => {
    const code = $("stats-code-input").value.trim();
    if (!/^[0-9a-zA-Z]{7}$/.test(code)) {
        toast("Код должен состоять ровно из 7 символов");
        return;
    }
    openModal(code);
});

/* закрытие: клик по фону, Esc */
modal.addEventListener("click", (event) => {
    if (event.target === modal) closeModal();
});
document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !modal.classList.contains("hidden")) closeModal();
});

/* ---------- 4) Удаление ссылки ---------- */

$("delete-btn").addEventListener("click", async () => {
    if (!currentCode) return;
    if (!window.confirm("Удалить ссылку " + currentCode + " вместе со всей статистикой?")) return;

    try {
        const res = await fetch("/api/v1/links/delete/" + encodeURIComponent(currentCode), {
            method: "DELETE",
        });
        if (res.ok) {
            resultBox.classList.add("hidden");
            currentCode = null;
            toast("Ссылка удалена. Она прожила яркую, но короткую жизнь");
        } else {
            const err = await res.json().catch(() => ({}));
            showError(errorBox, extractErrorMessage(err.detail, "Не удалось удалить"));
        }
    } catch (_e) {
        showError(errorBox, "Сервер недоступен");
    }
});

/* ---------- 5) Пасхалки ---------- */

/* Пасхалка №1: 10 кликов по логотипу — 🍋 превращается в 🧅.
   «Временные ссылки как лук: снимешь слой — прослезишься, что ссылка истекла». */
(function logoClicks() {
    const btn = $("logo-btn");
    const emoji = $("logo-emoji");
    let clicks = 0;
    const phrases = [
        "сокращаю…", "ещё чуть-чуть…", "не останавливайся…", "почти…",
        "ты уверен, что тебе это нужно?", "ладно, ещё разок…", "…",
        "там точно что-то есть?", "последний шанс…", "ну всё!",
    ];
    btn.addEventListener("click", () => {
        clicks += 1;
        if (clicks < 10) {
            if (clicks >= 3) toast(phrases[clicks - 3]);
            return;
        }
        emoji.textContent = "\uD83E\uDDC5"; /* 🧅 */
        emoji.classList.add("onion");
        toast("Поздравляем! Теперь у вас лук. Слои исчезают, как временные ссылки");
        clicks = 0;
    });
})();

/* Пасхалка №2: код Konami — обратный отсчёт до «самоуничтожения» (шутка) */
(function konami() {
    const SEQ = [
        "ArrowUp", "ArrowUp", "ArrowDown", "ArrowDown",
        "ArrowLeft", "ArrowRight", "ArrowLeft", "ArrowRight", "b", "a",
    ];
    let idx = 0;
    document.addEventListener("keydown", (event) => {
        idx = event.key === SEQ[idx] ? idx + 1 : (event.key === SEQ[0] ? 1 : 0);
        if (idx === SEQ.length) {
            idx = 0;
            toast("Self-destruct activated. Just kidding — ссылки и так временные \u{1F9A9}");
        }
    });
})();
