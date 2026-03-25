#!/usr/bin/env bash
set -euo pipefail

APP_NAME="api-web-tgbot"
SERVICE_NAME="${APP_NAME}.service"
CONFIG_DIR="/etc/${APP_NAME}"
RUNTIME_ENV="${CONFIG_DIR}/runtime.env"

INSTALL_DIR_DEFAULT="/opt/${APP_NAME}"
DATA_DIR_DEFAULT="/data/${APP_NAME}"
PORT_DEFAULT="8088"

GREEN='\033[32m'
YELLOW='\033[33m'
RED='\033[31m'
RESET='\033[0m'

log_info() { echo -e "${GREEN}[信息]${RESET} $*"; }
log_warn() { echo -e "${YELLOW}[提示]${RESET} $*"; }
log_err() { echo -e "${RED}[错误]${RESET} $*"; }

ensure_root() {
  if [[ "${EUID}" -ne 0 ]]; then
    log_err "请使用 root 或 sudo 运行"
    exit 1
  fi
}

load_runtime() {
  if [[ -f "${RUNTIME_ENV}" ]]; then
    # shellcheck disable=SC1090
    source "${RUNTIME_ENV}"
  fi
}

write_runtime() {
  mkdir -p "${CONFIG_DIR}"
  cat > "${RUNTIME_ENV}" <<EOT
INSTALL_DIR=${INSTALL_DIR}
DATA_DIR=${DATA_DIR}
PORT=${PORT}
EOT
}

print_header() {
  echo "============================================================"
  echo "API-WEB-TGBOT 管理菜单"
  echo "============================================================"
  echo "程序目录: ${INSTALL_DIR:-${INSTALL_DIR_DEFAULT}}"
  echo "数据目录: ${DATA_DIR:-${DATA_DIR_DEFAULT}}"
  echo "数据库文件: ${DATA_DIR:-${DATA_DIR_DEFAULT}}/manager.db"
  echo "服务端口: ${PORT:-${PORT_DEFAULT}}"
  echo
}

ensure_go() {
  if command -v go >/dev/null 2>&1; then
    return 0
  fi

  log_warn "未检测到 Go，开始自动安装..."
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -y
    DEBIAN_FRONTEND=noninteractive apt-get install -y golang-go
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y golang
  elif command -v yum >/dev/null 2>&1; then
    yum install -y golang
  else
    log_err "当前系统无 apt/dnf/yum，无法自动安装 Go"
    exit 1
  fi
}

install_app() {
  ensure_root
  local src_dir="${1:-$(pwd)}"

  if [[ -f "${RUNTIME_ENV}" ]]; then
    load_runtime
    log_warn "检测到已安装，可直接运行管理菜单；如需重装请先执行 uninstall"
    return 0
  fi

  read -r -p "安装目录 [${INSTALL_DIR_DEFAULT}]: " INSTALL_DIR
  INSTALL_DIR="${INSTALL_DIR:-${INSTALL_DIR_DEFAULT}}"

  read -r -p "数据目录(数据库和日志) [${DATA_DIR_DEFAULT}]: " DATA_DIR
  DATA_DIR="${DATA_DIR:-${DATA_DIR_DEFAULT}}"

  read -r -p "监听端口 [${PORT_DEFAULT}]: " PORT
  PORT="${PORT:-${PORT_DEFAULT}}"

  ensure_go

  mkdir -p "${INSTALL_DIR}" "${DATA_DIR}" "${CONFIG_DIR}"

  log_info "编译程序中..."
  (cd "${src_dir}" && go mod tidy && CGO_ENABLED=1 go build -o "${INSTALL_DIR}/${APP_NAME}" .)

  rm -rf "${INSTALL_DIR}/web"
  cp -R "${src_dir}/web" "${INSTALL_DIR}/web"

  write_runtime

  cat > "/etc/systemd/system/${SERVICE_NAME}" <<EOT
[Unit]
Description=API WEB TG Bot Manager
After=network.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
Environment=DATA_DIR=${DATA_DIR}
Environment=PORT=${PORT}
Environment=GIN_MODE=release
ExecStart=${INSTALL_DIR}/${APP_NAME}
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOT

  systemctl daemon-reload
  systemctl enable "${SERVICE_NAME}" >/dev/null 2>&1 || true
  systemctl restart "${SERVICE_NAME}"

  log_info "安装完成"
  log_info "访问地址: http://服务器IP:${PORT}"
}

start_app() {
  ensure_root
  systemctl start "${SERVICE_NAME}"
  log_info "已启动"
}

stop_app() {
  ensure_root
  systemctl stop "${SERVICE_NAME}"
  log_info "已停止"
}

restart_app() {
  ensure_root
  systemctl restart "${SERVICE_NAME}"
  log_info "已重启"
}

status_app() {
  ensure_root
  systemctl status "${SERVICE_NAME}" --no-pager || true
}

backup_db() {
  ensure_root
  load_runtime
  if [[ -z "${DATA_DIR:-}" ]]; then
    log_err "未安装"
    exit 1
  fi

  mkdir -p "${DATA_DIR}/backups"
  local src_db="${DATA_DIR}/manager.db"
  if [[ ! -f "${src_db}" ]]; then
    log_err "数据库文件不存在: ${src_db}"
    exit 1
  fi
  local ts
  ts="$(date +%Y%m%d_%H%M%S)"
  local out="${DATA_DIR}/backups/manager_${ts}.db"
  cp -f "${src_db}" "${out}"
  log_info "备份完成: ${out}"
}

restore_db() {
  ensure_root
  load_runtime
  local arg="${1:-latest}"
  local target=""

  if [[ "${arg}" == "latest" ]]; then
    target="$(ls -1t "${DATA_DIR}/backups"/manager_*.db 2>/dev/null | head -n1 || true)"
  else
    target="${arg}"
  fi

  if [[ -z "${target}" || ! -f "${target}" ]]; then
    log_err "未找到备份文件"
    exit 1
  fi

  systemctl stop "${SERVICE_NAME}" || true
  cp -f "${target}" "${DATA_DIR}/manager.db"
  systemctl start "${SERVICE_NAME}"
  log_info "还原完成: ${target}"
}

clear_db() {
  ensure_root
  load_runtime
  systemctl stop "${SERVICE_NAME}" || true
  rm -f "${DATA_DIR}/manager.db"
  systemctl start "${SERVICE_NAME}"
  log_info "已清空数据库，系统恢复到初始登录状态（admin/admin）"
}

uninstall_app() {
  ensure_root
  load_runtime

  systemctl stop "${SERVICE_NAME}" >/dev/null 2>&1 || true
  systemctl disable "${SERVICE_NAME}" >/dev/null 2>&1 || true
  rm -f "/etc/systemd/system/${SERVICE_NAME}"
  systemctl daemon-reload

  if [[ -n "${INSTALL_DIR:-}" && -d "${INSTALL_DIR}" ]]; then
    rm -rf "${INSTALL_DIR}"
  fi

  rm -f "${RUNTIME_ENV}"

  log_info "卸载完成，已保留数据目录和日志: ${DATA_DIR:-${DATA_DIR_DEFAULT}}"
}

menu() {
  ensure_root
  load_runtime

  if [[ ! -f "${RUNTIME_ENV}" ]]; then
    print_header
    echo "未检测到安装，进入一键安装流程..."
    install_app "${1:-$(pwd)}"
    load_runtime
  fi

  while true; do
    print_header
    echo "  1) 启动服务"
    echo "  2) 停止服务"
    echo "  3) 重启服务"
    echo "  4) 查看状态"
    echo "  5) 备份数据库"
    echo "  6) 还原数据库"
    echo "  7) 清空数据库"
    echo "  8) 卸载程序"
    echo "  0) 退出"
    echo
    read -r -p "请选择操作: " opt
    case "${opt}" in
      1) start_app ;;
      2) stop_app ;;
      3) restart_app ;;
      4) status_app ;;
      5) backup_db ;;
      6)
        read -r -p "输入备份路径(直接回车为 latest): " p
        restore_db "${p:-latest}"
        ;;
      7)
        read -r -p "确认清空数据库？输入 YES: " c
        [[ "${c}" == "YES" ]] && clear_db || log_warn "已取消"
        ;;
      8)
        read -r -p "确认卸载？输入 YES: " c
        [[ "${c}" == "YES" ]] && { uninstall_app; break; } || log_warn "已取消"
        ;;
      0) exit 0 ;;
      *) log_warn "无效选项" ;;
    esac
    read -r -p "按回车返回菜单..." _
  done
}

usage() {
  cat <<EOT
用法:
  bash scripts/jdc_manager.sh                  # 交互菜单（未安装则先安装）
  bash scripts/jdc_manager.sh install [src_dir]
  bash scripts/jdc_manager.sh start|stop|restart|status
  bash scripts/jdc_manager.sh backup
  bash scripts/jdc_manager.sh restore [备份文件路径|latest]
  bash scripts/jdc_manager.sh clear-db
  bash scripts/jdc_manager.sh uninstall
EOT
}

cmd="${1:-menu}"
case "${cmd}" in
  install)
    shift || true
    install_app "${1:-$(pwd)}"
    ;;
  start) start_app ;;
  stop) stop_app ;;
  restart) restart_app ;;
  status) status_app ;;
  backup) backup_db ;;
  restore)
    shift || true
    restore_db "${1:-latest}"
    ;;
  clear-db) clear_db ;;
  uninstall) uninstall_app ;;
  menu)
    shift || true
    menu "${1:-$(pwd)}"
    ;;
  *)
    usage
    exit 1
    ;;
esac
