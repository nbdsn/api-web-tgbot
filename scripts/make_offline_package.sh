#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="${1:-$ROOT_DIR/dist}"
DATE_TAG="$(date +%Y%m%d)"
OUT_FILE="${OUT_DIR}/api-web-tgbot-offline-${DATE_TAG}.tar.gz"

mkdir -p "${OUT_DIR}"

# 打离线源码包（不包含 .git 和临时文件）
tar -czf "${OUT_FILE}" \
  --exclude='.git' \
  --exclude='dist' \
  --exclude='*.log' \
  -C "${ROOT_DIR}" .

SHA256="$(shasum -a 256 "${OUT_FILE}" | awk '{print $1}')"

echo "离线安装包已生成: ${OUT_FILE}"
echo "SHA256: ${SHA256}"
