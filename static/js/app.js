"use strict";

const $ = (id) => document.getElementById(id);

const urlInput = $("url-input");
const form = $("shorten-form");
const errorBox = $("error-box");
const resultBox = $("result");
const shortLink = $("short-link");
const copyBtn = $("copy-btn");

function showError(message) {
    errorBox.textContent = message;
    errorBox.classList.remove("hidden");
}

function hideError() {
    errorBox.classList.add("hidden");
}

form.addEventListener("submit", async (event) => {
    event.preventDefault();
    hideError();
    resultBox.classList.add("hidden");

    const value = urlInput.value.trim();
    if (!value) {
        showError("Введите URL.");
        return;
    }

    const btn = $("shorten-btn");
    btn.disabled = true;
    btn.textContent = "Сокращаем...";
    try {
        const res = await fetch("/api/v2/links", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ original_url: value }),
        });
        const data = await res.json().catch(() => ({}));
        if (res.status !== 201) {
            showError(typeof data.detail === "string" ? data.detail : "Ошибка " + res.status);
            return;
        }
        shortLink.textContent = data.short_url;
        shortLink.href = data.short_url;
        resultBox.classList.remove("hidden");
        urlInput.select();
    } catch (_e) {
        showError("Сервер недоступен.");
    } finally {
        btn.disabled = false;
        btn.textContent = "Сократить";
    }
});

copyBtn.addEventListener("click", async () => {
    const text = shortLink.textContent;
    if (!text) return;
    try {
        await navigator.clipboard.writeText(text);
    } catch (_e) {
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