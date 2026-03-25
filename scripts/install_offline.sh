#!/usr/bin/env bash
set -euo pipefail

# 用法：
#   sudo bash scripts/install_offline.sh /tmp/api-web-tgbot-src
#   sudo bash scripts/install_offline.sh /tmp/api-web-tgbot.tar.gz

if [[ "${EUID}" -ne 0 ]]; then
  echo "请使用 root 或 sudo 运行"
  exit 1
fi

INPUT="${1:-}"
if [[ -z "${INPUT}" ]]; then
  echo "用法: sudo bash scripts/install_offline.sh <源码目录|tar.gz包路径>"
  exit 1
fi

WORK_DIR="/tmp/api-web-tgbot-offline-src"
rm -rf "${WORK_DIR}"
mkdir -p "${WORK_DIR}"

if [[ -d "${INPUT}" ]]; then
  cp -R "${INPUT}"/. "${WORK_DIR}"/
elif [[ -f "${INPUT}" ]]; then
  tar -xzf "${INPUT}" -C "${WORK_DIR}"
  if [[ -f "${WORK_DIR}/scripts/jdc_manager.sh" ]]; then
    :
  else
    inner="$(find "${WORK_DIR}" -maxdepth 2 -type f -name jdc_manager.sh | head -n1 || true)"
    if [[ -n "${inner}" ]]; then
      WORK_DIR="$(dirname "$(dirname "${inner}")")"
    fi
  fi
else
  echo "输入路径不存在: ${INPUT}"
  exit 1
fi

bash "${WORK_DIR}/scripts/jdc_manager.sh" menu "${WORK_DIR}"
