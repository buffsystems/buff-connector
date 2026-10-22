"""A PDF as text, page by page, through pypdf (pure Python, bundled with the
plugin). Only a text layer can be read: a scanned page is a picture, and the
answer says so rather than pretending it read nothing."""

from __future__ import annotations

import io

import pypdf
from pypdf.errors import PdfReadError


def text_of(data: bytes) -> str:
    try:
        reader = pypdf.PdfReader(io.BytesIO(data))
        if reader.is_encrypted:
            if reader.decrypt("") == pypdf.PasswordType.NOT_DECRYPTED:
                raise ValueError("PDF защищён паролем — прочитать нельзя")
        pages = [(page.extract_text() or "").strip() for page in reader.pages]
    except PdfReadError as exc:
        raise ValueError(f"PDF не разбирается: {exc}") from exc
    if not any(pages):
        raise ValueError(f"в PDF нет текстового слоя (скан или картинки), страниц: {len(pages)}")
    out: list[str] = []
    for n, text in enumerate(pages, 1):
        out += [f"## Страница {n}", "", text or "(без текста)", ""]
    return "\n".join(out) + "\n"
