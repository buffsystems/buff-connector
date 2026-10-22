#!/bin/bash
# Rebuilds the binary fixtures from their sources with a headless LibreOffice
# in a container, so the files the tests read are real Office/PDF output, not
# something hand-assembled to fit the reader.
#
#   tests/fixtures/make-fixtures.sh          # needs docker
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE=lo-convert:local
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  docker build -t "$IMAGE" - <<'DOCKERFILE'
FROM debian:bookworm-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    libreoffice-writer-nogui libreoffice-calc-nogui libreoffice-impress-nogui \
    fonts-dejavu fonts-liberation && rm -rf /var/lib/apt/lists/*
ENTRYPOINT ["soffice", "--headless", "--norestore"]
DOCKERFILE
fi
conv() { docker run --rm -v "$HERE/src:/in:ro" -v "$HERE:/out" "$IMAGE" "$@" --outdir /out; }
conv --infilter="HTML (StarWriter)" --convert-to docx:"MS Word 2007 XML" /in/regulations.html
conv --infilter="HTML (StarWriter)" --convert-to doc:"MS Word 97" /in/instruction.html
conv --infilter="HTML (StarWriter)" --convert-to pdf:writer_pdf_Export /in/contract.html
conv --convert-to xlsx /in/budget.fods
conv --convert-to xls /in/budget.fods && mv "$HERE/budget.xls" "$HERE/budget-2019.xls"
conv --convert-to xls /in/limits-2019.fods
conv --convert-to pptx /in/roadmap.fodp
conv --convert-to ppt /in/kickoff-2018.fodp
