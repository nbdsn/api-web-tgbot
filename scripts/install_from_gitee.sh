#!/usr/bin/env bash
set -euo pipefail

# 用法：
#   GITEE_REPO_URL=https://gitee.com/<你的账号>/<仓库名>.git \
#   sudo bash scripts/install_from_gitee.sh

REPO_URL="${GITEE_REPO_URL:-}"
BRANCH="${GITEE_BRANCH:-main}"
WORK_DIR="${WORK_DIR:-/tmp/api-web-tgbot-src}"

if [[ "${EUID}" -ne 0 ]]; then
  echo "请使用 root 或 sudo 运行"
  exit 1
fi

if [[ -z "${REPO_URL}" ]]; then
  cat <<EOT
[错误] 未设置 GITEE_REPO_URL
示例：
  GITEE_REPO_URL=https://gitee.com/nbdsn/api-web-tgbot.git sudo bash scripts/install_from_gitee.sh
EOT
  exit 1
fi

rm -rf "${WORK_DIR}"
mkdir -p "${WORK_DIR}"

echo "[信息] 拉取 Gitee 源码..."
if ! command -v git >/dev/null 2>&1; then
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
fi

git clone --depth 1 -b "${BRANCH}" "${REPO_URL}" "${WORK_DIR}"

echo "[信息] 进入安装/管理菜单..."
bash "${WORK_DIR}/scripts/jdc_manager.sh" menu "${WORK_DIR}"
