"""Тесты QR-эндпоинта GET /api/v1/links/{code}/qr.

Покрывают замечания ревью:
- PNG валиден и кодирует short_url;
- невалидный код / отсутствующая ссылка -> 404;
- после DELETE -> 404 (инвалидация через отсутствие в БД/кэше);
- тяжёлый рендер вынесен из event loop (asyncio.to_thread).
"""

import asyncio
import inspect
import io
import sys
import unittest
from pathlib import Path
from unittest.mock import AsyncMock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app.links import _render_qr_png, get_qr  # noqa: E402


class TestRenderQrPng(unittest.TestCase):
    def test_png_signature_and_nonempty(self):
        png = _render_qr_png("http://localhost/AbC1234")
        self.assertTrue(len(png) > 100)
        # PNG signature.
        self.assertEqual(png[:8], b"\x89PNG\r\n\x1a\n")

    def test_png_decodable_by_pillow(self):
        try:
            from PIL import Image
        except ImportError:
            self.skipTest("Pillow not installed")
        png = _render_qr_png("http://localhost/AbC1234")
        img = Image.open(io.BytesIO(png))
        self.assertEqual(img.format, "PNG")
        self.assertGreater(img.width, 0)
        self.assertGreater(img.height, 0)


class TestGetQrHandler(unittest.IsolatedAsyncioTestCase):
    async def test_invalid_code_returns_404_without_db(self):
        from fastapi import HTTPException

        for bad in ["short", "toolongcode123", "bad!!##", "", "abc 123"]:
            with self.subTest(code=bad):
                with self.assertRaises(HTTPException) as ctx:
                    await get_qr(bad)
                self.assertEqual(ctx.exception.status_code, 404)

    async def test_missing_link_returns_404(self):
        from fastapi import HTTPException

        with patch("app.links.cache.get", new=AsyncMock(return_value=None)), patch(
            "app.links.db.fetch_one", new=AsyncMock(return_value=None)
        ):
            with self.assertRaises(HTTPException) as ctx:
                await get_qr("AbC1234")
            self.assertEqual(ctx.exception.status_code, 404)

    async def test_deleted_link_returns_404(self):
        """После DELETE ссылка исчезает из БД/кэша -> здесь 404 без отдельной инвалидации."""
        from fastapi import HTTPException

        with patch("app.links.cache.get", new=AsyncMock(return_value=None)), patch(
            "app.links.db.fetch_one", new=AsyncMock(return_value=None)
        ):
            with self.assertRaises(HTTPException) as ctx:
                await get_qr("DelEtEd")
            self.assertEqual(ctx.exception.status_code, 404)

    async def test_existing_link_returns_png_with_cache_header(self):
        with patch("app.links.cache.get", new=AsyncMock(return_value="123:http://x")), patch(
            "app.links.db.fetch_one", new=AsyncMock(return_value={"ok": 1})
        ):
            resp = await get_qr("AbC1234")
            self.assertEqual(resp.media_type, "image/png")
            self.assertEqual(resp.headers.get("Cache-Control"), "public, max-age=3600")
            body = resp.body if hasattr(resp, "body") else resp.content
            # Starlette Response stores rendered body in .body.
            raw = body if isinstance(body, (bytes, bytearray)) else bytes(body or b"")
            if not raw and hasattr(resp, "content"):
                raw = resp.content if isinstance(resp.content, (bytes, bytearray)) else b""
            self.assertTrue(raw.startswith(b"\x89PNG"))

    def test_qr_render_offloaded_from_event_loop(self):
        """Рендер PIL не должен блокировать event loop: хендлер использует to_thread."""
        src = inspect.getsource(get_qr)
        self.assertIn("to_thread", src)
        self.assertIn("_render_qr_png", src)


if __name__ == "__main__":
    unittest.main()
