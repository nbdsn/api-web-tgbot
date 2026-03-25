# api-web-tgbot

独立版 NewAPI 管理助手（Web + Telegram Bot）。

这个项目和主程序解耦，不改主程序代码，直接通过主程序 API 做管理操作。

## 核心能力

- 独立登录后台（默认 `admin/admin`，可在后台改账号密码）
- 主程序连接配置页面（地址、账号、密码）
- 主程序数据库路径配置（可手动填写，支持一键搜索）
- TG 配置页面（Bot Token、管理员 ID 逗号分隔、轮询间隔、额度换算比例）
- 渠道管理页面（新增、刷新、启停、删除）
- TG 命令与二级交互菜单
- 安装管理脚本（安装、启动、停止、重启、备份、还原、清空数据库、卸载）
- Docker 构建与 `docker-compose` 运行

## TG 命令

- `/start` `/help`：显示命令说明
- `/stats`：详细统计（用户数、请求次数、统计次数、统计额度、Tokens、平均 RPM、实时 RPM/TPM）
- `/users`：用户概览（额度最低前 10）+ 二级菜单（加减额度、启用停用）
- `/redeem`：交互生成兑换码（先选金额再选数量）

## 一键安装（Ubuntu / Debian / CentOS）

在目标服务器执行：

```bash
sudo bash <(curl -fsSL https://raw.githubusercontent.com/nbdsn/api-web-tgbot/main/scripts/install_from_github.sh)
```

执行后行为：

- 未安装：进入交互安装（可自定义安装目录、数据目录、端口，回车使用默认）
- 已安装：进入管理菜单（启动/停止/重启/状态/备份/还原/清空数据库/卸载）

## 本地开发运行

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

构建并运行：

```bash
docker compose up -d --build
```

默认映射：

- `8088:8088`
- `./data:/data/api-web-tgbot`

## 目录说明

- `main.go`：后端服务（登录、配置、主程序 API 代理、TG 轮询与命令）
- `web/login.html`：登录页
- `web/index.html`：管理后台
- `scripts/jdc_manager.sh`：安装/运维菜单脚本
- `scripts/install_from_github.sh`：远程一键安装入口脚本
- `.github/workflows/build.yml`：GitHub Actions（Linux 构建 + Docker 构建）

## 重要说明

- 这是独立项目，不依赖旧仓库发布包。
- 渠道、用户、兑换码操作是通过主程序 API 执行，需先在“主程序连接配置”里设置正确地址和管理员账号密码。
- TG 管理员 ID 支持多个，使用英文逗号分割，例如：`123456789,987654321`。
