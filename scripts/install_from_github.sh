#!/usr/bin/env bash
set -euo pipefail

REPO_URL="https://github.com/nbdsn/api-web-tgbot.git"
BRANCH="main"
WORK_DIR="/tmp/api-web-tgbot-src"

if [[ "${EUID}" -ne 0 ]]; then
  echo "请使用 root 或 sudo 运行"
  exit 1
fi

rm -rf "${WORK_DIR}"
mkdir -p "${WORK_DIR}"

echo "[信息] 拉取项目源码..."
if command -v git >/dev/null 2>&1; then
  git clone --depth 1 -b "${BRANCH}" "${REPO_URL}" "${WORK_DIR}"
else
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -y
    DEBIAN_FRONTEND=noninteractive apt-get install -y git
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y git
  elif command -v yum >/dev/null 2>&1; then
    yum install -y git
  else
    echo "[错误] 无法安装 git，请先手动安装"
    exit 1
  fi
  git clone --depth 1 -b "${BRANCH}" "${REPO_URL}" "${WORK_DIR}"
fi

echo "[信息] 进入安装/管理菜单..."
bash "${WORK_DIR}/scripts/jdc_manager.sh" menu "${WORK_DIR}"
