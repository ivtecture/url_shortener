"use strict";

// Все пути — только к активному API /api/v2/. Legacy /api/v1-legacy/*
// фронтендом не используется.

const API = "/api/v2";
const TYPING_PLACEHOLDER = "https://example.com/very/long/url";

const $ = (id) => document.getElementById(id);

const urlInput = $("url-input");
const form = $("shorten-form");
const errorBox = $("error-box");
const resultBox = $("result");
const shortLink = $("short-link");
const shortenBtn = $("shorten-btn");
const copyBtn = $("copy-btn");
const statsBtn = $("stats-btn");
const deleteBtn = $("delete-btn");
const statsCodeInput = $("stats-code-input");
const statsCodeBtn = $("stats-code-btn");

const modal = $("stats-modal");
const modalError = $("modal-error");
const modalCodeLabel = $("modal-code-label");
const modalCreated = $("modal-created");
const modalOriginal = $("modal-original");
const modalClicks = $("modal-clicks");
const modalCountries = $("modal-countries");
const modalAds = $("modal-ads");
const modalRefreshBtn = $("modal-refresh-btn");

let currentCode = null;
let modalCode = null;
let lastFocused = null;

function showError(box, message) {
    box.textContent = message;
    box.classList.remove("hidden");
}

function hideError(box) {
    box.classList.add("hidden");
    box.textContent = "";
}

// detail бывает строкой (Go) и списком объектов (legacy-валидация pydantic).
function extractErrorMessage(detail, fallback) {
    if (typeof detail === "string") return detail;
    if (Array.isArray(detail) && detail.length > 0) {
        const msg = detail[0] && detail[0].msg;
        if (typeof msg === "string") return msg;
    }
    return fallback;
}

async function readErrorBody(res) {
    const data = await res.json().catch(() => ({}));
    return extractErrorMessage(data.detail, "Ошибка " + res.status);
}

/* ------------------------------------------------------------------ */
/* Печатающийся placeholder                                             */
/* ------------------------------------------------------------------ */

// При prefers-reduced-motion посимвольная печать отключается целиком:
// пользователь сразу видит обычный placeholder.
const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

let typingTimer = null;
let typingIndex = 0;

function stopTyping() {
    window.clearTimeout(typingTimer);
    typingTimer = null;
    typingIndex = 0;
    urlInput.placeholder = "";
    const hint = document.querySelector(".typing-hint");
    if (hint) hint.remove();
}

function startTyping() {
    stopTyping();
    urlInput.placeholder = "";
    const hint = document.createElement("span");
    hint.className = "typing-hint";
    hint.setAttribute("aria-hidden", "true");
    hint.textContent = TYPING_PLACEHOLDER.slice(0, 1);
    urlInput.parentElement.appendChild(hint);

    typingTimer = window.setInterval(() => {
        typingIndex += 1;
        if (typingIndex >= TYPING_PLACEHOLDER.length) {
            stopTyping();
            urlInput.placeholder = TYPING_PLACEHOLDER;
            return;
        }
        hint.textContent = TYPING_PLACEHOLDER.slice(0, typingIndex + 1);
    }, 90);
}

urlInput.addEventListener("focus", () => {
    if (urlInput.value) return;
    if (reduceMotion.matches) {
        // Печать посимвольно отключена — показываем готовый placeholder.
        urlInput.placeholder = TYPING_PLACEHOLDER;
        return;
    }
    startTyping();
});

urlInput.addEventListener("input", stopTyping);
urlInput.addEventListener("blur", stopTyping);

// Переключение настроек без перезагрузки: останавливаем анимацию.
if (typeof reduceMotion.addEventListener === "function") {
    reduceMotion.addEventListener("change", () => {
        stopTyping();
        if (!urlInput.value) {
            urlInput.placeholder = reduceMotion.matches ? TYPING_PLACEHOLDER : "";
        }
    });
}

/* ------------------------------------------------------------------ */
/* Создание ссылки                                                      */
/* ------------------------------------------------------------------ */

form.addEventListener("submit", async (event) => {
    event.preventDefault();
    hideError(errorBox);
    resultBox.classList.add("hidden");
    currentCode = null;

    const value = urlInput.value.trim();
    if (!value) {
        showError(errorBox, "Введите URL.");
        urlInput.focus();
        return;
    }

    shortenBtn.disabled = true;
    shortenBtn.textContent = "Сокращаем...";
    try {
        const res = await fetch(API + "/links", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ original_url: value }),
        });
        if (res.status !== 201) {
            showError(errorBox, await readErrorBody(res));
            return;
        }
        const data = await res.json();
        currentCode = data.short_code;
        shortLink.textContent = data.short_url;
        shortLink.href = data.short_url;
        resultBox.classList.remove("hidden");
        resultBox.scrollIntoView({ block: "nearest" });
    } catch (_e) {
        showError(errorBox, "Сервер недоступен.");
    } finally {
        shortenBtn.disabled = false;
        shortenBtn.textContent = "Сократить!";
    }
});

/* ------------------------------------------------------------------ */
/* Копирование                                                          */
/* ------------------------------------------------------------------ */

copyBtn.addEventListener("click", async () => {
    const text = shortLink.textContent;
    if (!text) return;
    try {
        await navigator.clipboard.writeText(text);
    } catch (_e) {
        // Clipboard API доступен не во всех контекстах (http без TLS) —
        // откатываемся на execCommand.
        const ta = document.createElement("textarea");
        ta.value = text;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
    }
    const old = copyBtn.textContent;
    copyBtn.textContent = "Скопировано";
    window.setTimeout(() => (copyBtn.textContent = old), 1500);
});

/* ------------------------------------------------------------------ */
/* Удаление                                                             */
/* ------------------------------------------------------------------ */

deleteBtn.addEventListener("click", async () => {
    if (!currentCode) return;
    if (!window.confirm("Удалить ссылку " + currentCode + "?")) return;

    hideError(errorBox);
    try {
        const res = await fetch(API + "/links/delete/" + encodeURIComponent(currentCode), {
            method: "DELETE",
        });
        if (!res.ok) {
            showError(errorBox, await readErrorBody(res));
            return;
        }
        resultBox.classList.add("hidden");
        shortLink.textContent = "";
        shortLink.href = "#";
        urlInput.value = "";
        currentCode = null;
    } catch (_e) {
        showError(errorBox, "Сервер недоступен.");
    }
});

/* ------------------------------------------------------------------ */
/* Модалка статистики                                                   */
/* ------------------------------------------------------------------ */

function openModal(code) {
    // Запоминаем элемент, который открыл модалку, только если он вне неё:
    // иначе повторное открытие запомнило бы кнопку внутри модалки и после
    // закрытия фокус упал бы на <body>.
    const active = document.activeElement;
    if (!modal.contains(active)) lastFocused = active;
    modalCode = code;
    modal.classList.remove("hidden");
    hideError(modalError);
    modalRefreshBtn.focus();
    loadStats();
}

function closeModal() {
    modal.classList.add("hidden");
    modalCode = null;
    if (lastFocused && typeof lastFocused.focus === "function") lastFocused.focus();
}

function renderCountries(countries) {
    modalCountries.textContent = "";
    if (!Array.isArray(countries) || countries.length === 0) {
        const tr = document.createElement("tr");
        const td = document.createElement("td");
        td.colSpan = 2;
        td.className = "empty-cell";
        td.textContent = "нет данных";
        tr.appendChild(td);
        modalCountries.appendChild(tr);
        return;
    }
    for (const item of countries) {
        const tr = document.createElement("tr");
        const tdCountry = document.createElement("td");
        tdCountry.className = "mono";
        tdCountry.textContent = item.country || "unknown";
        const tdCount = document.createElement("td");
        tdCount.className = "mono";
        tdCount.textContent = String(item.count);
        tr.appendChild(tdCountry);
        tr.appendChild(tdCount);
        modalCountries.appendChild(tr);
    }
}

function renderAds(ads) {
    modalAds.textContent = "";
    if (!ads) {
        const tr = document.createElement("tr");
        const td = document.createElement("td");
        td.colSpan = 3;
        td.className = "empty-cell";
        td.textContent = "нет данных";
        tr.appendChild(td);
        modalAds.appendChild(tr);
        return;
    }
    const cells = [
        String(ads.impressions),
        String(ads.clicks),
        (ads.ctr * 100).toFixed(2) + "%",
    ];
    const tr = document.createElement("tr");
    for (const value of cells) {
        const td = document.createElement("td");
        td.className = "mono";
        td.textContent = value;
        tr.appendChild(td);
    }
    modalAds.appendChild(tr);
}

async function loadStats() {
    if (!modalCode) return;
    hideError(modalError);
    modalCodeLabel.textContent = modalCode;

    try {
        const [statsRes, adsRes] = await Promise.all([
            fetch(API + "/links/" + encodeURIComponent(modalCode) + "/stats"),
            fetch(API + "/links/" + encodeURIComponent(modalCode) + "/ads").catch(() => null),
        ]);

        if (!statsRes.ok) {
            showError(modalError, await readErrorBody(statsRes));
            return;
        }

        const stats = await statsRes.json();
        modalCodeLabel.textContent = stats.short_code || modalCode;
        modalCreated.textContent = stats.created_at || "—";
        modalOriginal.textContent = stats.original_url || "—";
        modalClicks.textContent = String(stats.clicks);
        renderCountries(stats.countries);

        renderAds(adsRes && adsRes.ok ? await adsRes.json() : null);
    } catch (_e) {
        showError(modalError, "Сервер недоступен.");
    }
}

statsBtn.addEventListener("click", () => {
    if (currentCode) openModal(currentCode);
});

statsCodeBtn.addEventListener("click", () => {
    const code = statsCodeInput.value.trim();
    if (code.length !== 7) {
        showError(errorBox, "Код состоит из 7 символов.");
        return;
    }
    hideError(errorBox);
    openModal(code);
});

statsCodeInput.addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
        event.preventDefault();
        statsCodeBtn.click();
    }
});

modalRefreshBtn.addEventListener("click", loadStats);
$("modal-close-x").addEventListener("click", closeModal);
$("modal-close-btn").addEventListener("click", closeModal);

modal.addEventListener("click", (event) => {
    if (event.target === modal) closeModal();
});

// Ловушка фокуса: пока модалка открыта, Tab не должен уводить фокус
// на страницу под ней — иначе клавиатурный пользователь «теряется».
const FOCUSABLE = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';

function modalFocusables() {
    return Array.from(modal.querySelectorAll(FOCUSABLE)).filter(
        (el) => el.offsetParent !== null && !el.disabled,
    );
}

document.addEventListener("keydown", (event) => {
    if (modal.classList.contains("hidden")) return;

    if (event.key === "Escape") {
        closeModal();
        return;
    }

    if (event.key !== "Tab") return;

    const items = modalFocusables();
    if (items.length === 0) return;
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;

    if (!modal.contains(active)) {
        event.preventDefault();
        first.focus();
        return;
    }
    if (event.shiftKey && active === first) {
        event.preventDefault();
        last.focus();
    } else if (!event.shiftKey && active === last) {
        event.preventDefault();
        first.focus();
    }
});