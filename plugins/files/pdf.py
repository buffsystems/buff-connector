"""A PDF as text, page by page, through pypdf (pure Python, bundled with the
plugin). Only a text layer can be read: a scanned page is a picture, and the
answer says so rather than pretending it read nothing."""

from __future__ import annotations

import io

import pypdf
from pypdf.errors import PdfReadError

from buff_plugin import say


def text_of(data: bytes) -> str:
    try:
        reader = pypdf.PdfReader(io.BytesIO(data))
        if reader.is_encrypted:
            if reader.decrypt("") == pypdf.PasswordType.NOT_DECRYPTED:
                raise ValueError(say("PDF защищён паролем — прочитать нельзя", "the PDF is password-protected — it cannot be read"))
        pages = [(page.extract_text() or "").strip() for page in reader.pages]
    except PdfReadError as exc:
        raise ValueError(say(f"PDF не разбирается: {exc}", f"the PDF does not parse: {exc}")) from exc
    if not any(pages):
        raise ValueError(say(f"в PDF нет текстового слоя (скан или картинки), страниц: {len(pages)}",
                             f"the PDF has no text layer (a scan or pictures), pages: {len(pages)}"))
    out: list[str] = []
    for n, text in enumerate(pages, 1):
        out += [say(f"## Страница {n}", f"## Page {n}"), "", text or say("(без текста)", "(no text)"), ""]
    return "\n".join(out) + "\n"
