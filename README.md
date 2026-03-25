# api-web-tgbot

独立版 NewAPI 管理助手（Web + Telegram Bot）。

项目与主程序解耦，不改主程序代码，通过主程序 API 完成管理操作。

## 核心能力

- 独立登录后台（默认 `admin/admin`，支持后台修改账号密码）
- 主程序连接配置（地址、账号、密码）
- 数据库模式开关（默认不使用；开启后可选数据库路径并支持一键搜索）
- API 可用性校验（不仅测试连通，还会校验用户/渠道接口是否可操作）
- 代理配置（支持 `http://` 和 `socks5://`）
- TG 配置（Token、管理员 ID、轮询间隔、TG API Base）
- TG 测试消息发送
- TG 命令二级菜单（`/stats` `/users` `/redeem`）
- 渠道管理（新增、刷新、启停、删除）
- 每日自动额度处理（时间、阈值、目标值、白名单、管理员报告）
- 自动处理日志（Web 查看最近日志）
- 安装/运维菜单（安装、启动、停止、重启、状态、备份、还原、清库、卸载）
- Docker 构建与运行

## 功能依赖说明

- 渠道管理：仅依赖主程序 API
- TG 命令管理：仅依赖主程序 API
- 自动额度处理：仅依赖主程序 API
- 数据库路径：仅在你开启“数据库模式”时才需要

## 一键安装（在线）

```bash
sudo bash <(curl -fsSL https://raw.githubusercontent.com/nbdsn/api-web-tgbot/main/scripts/install_from_github.sh)
```

## 离线安装（无 Git 环境）

### 离线包下载链接

```text
https://github.com/nbdsn/api-web-tgbot/releases/latest/download/api-web-tgbot-offline-latest.tar.gz
```

> 如果首次访问是 404，表示你还没发布离线包，请先在本地执行 `bash scripts/make_offline_package.sh` 并上传到 Release。

### 安装命令

```bash
curl -fL https://github.com/nbdsn/api-web-tgbot/releases/latest/download/api-web-tgbot-offline-latest.tar.gz -o /tmp/api-web-tgbot-offline-latest.tar.gz
mkdir -p /tmp/api-web-tgbot-offline
tar -xzf /tmp/api-web-tgbot-offline-latest.tar.gz -C /tmp/api-web-tgbot-offline
sudo bash /tmp/api-web-tgbot-offline/scripts/jdc_manager.sh menu /tmp/api-web-tgbot-offline
```

## 本地开发

```bash
go mod tidy
go run .
```

默认：

- 监听：`8088`
- 数据目录：`./data`
- 管理数据库：`./data/manager.db`

环境变量：

- `PORT`：监听端口
- `DATA_DIR`：本地数据目录
- `SESSION_SECRET`：会话密钥

## Docker

```bash
docker compose up -d --build
```

默认映射：

- `8088:8088`
- `./data:/data/api-web-tgbot`

## 目录说明

- `main.go`：后端服务
- `web/login.html`：登录页
- `web/index.html`：管理后台
- `scripts/jdc_manager.sh`：安装/运维菜单脚本
- `scripts/install_from_github.sh`：在线安装入口
- `scripts/make_offline_package.sh`：离线包打包脚本
- `.github/workflows/build.yml`：构建与 Docker 工作流
