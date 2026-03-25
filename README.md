# api-web-tgbot

独立版 NewAPI 管理助手（Web + Telegram Bot），不改主程序代码，通过主程序 API 管理。

## 功能
- 独立后台登录（默认 `admin/admin`，可修改）
- 主程序连接配置（非数据库模式默认开启）
- 可选数据库模式（仅在你明确要扫库时开启）
- TG 管理（命令菜单、用户管理、兑换码、统计）
- 每日自动额度处理 + 日志 + TG 推送
- 渠道管理
- 代理支持（HTTP/SOCKS5）

## 安装方式（只保留两种）

### 1) 海外服务器（可访问 GitHub）一键安装
直接运行：

```bash
sudo bash <(curl -fsSL https://raw.githubusercontent.com/nbdsn/api-web-tgbot/main/scripts/install_from_github.sh)
```

执行后会进入交互菜单：
- 未安装：先提示输入安装目录/数据目录/端口（回车用默认）
- 已安装：直接进入管理菜单（启动/停止/重启/备份/还原/清库/卸载）

### 2) 国内服务器（无法拉 Git）离线安装

离线包下载地址（发布后可直接下载）：

```text
https://github.com/nbdsn/api-web-tgbot/releases/latest/download/api-web-tgbot-offline-latest.tar.gz
```

如果你手里已有离线包（例如 `api-web-tgbot-offline-20260325.tar.gz`），把它上传到服务器任意目录即可，推荐 `/tmp`。

#### 2.1 上传到服务器
可用 `scp`（在本地执行）：

```bash
scp -P <SSH端口> ./api-web-tgbot-offline-*.tar.gz root@<服务器IP>:/tmp/
```

#### 2.2 在服务器安装
在服务器执行：

```bash
mkdir -p /tmp/api-web-tgbot-offline
tar -xzf /tmp/api-web-tgbot-offline-*.tar.gz -C /tmp/api-web-tgbot-offline
sudo bash /tmp/api-web-tgbot-offline/scripts/jdc_manager.sh menu /tmp/api-web-tgbot-offline
```

## 管理命令
如果已安装，可直接运行：

```bash
sudo bash /tmp/api-web-tgbot-src/scripts/jdc_manager.sh
```

或者离线目录：

```bash
sudo bash /tmp/api-web-tgbot-offline/scripts/jdc_manager.sh
```

菜单支持：
- 启动/停止/重启/状态
- 备份数据库
- 还原数据库
- 清空数据库（恢复初始后台账号）
- 卸载程序（保留数据目录与日志）

## Docker（可选，更快部署）

### 推荐目录规划（与你当前需求一致）
- NewAPI 主程序数据目录：`/root/newapi`
- 本项目（api-web-tgbot）数据目录：`/root/newapi/web`

这样两套程序都在 `/root/newapi` 下，便于备份与迁移。

### 1) NewAPI 主程序一键命令（映射到 /root/newapi）

```bash
docker run --name new-api -d --restart always \
  -p 3000:3000 \
  -e TZ=Asia/Shanghai \
  -v /root/newapi:/data \
  calciumion/new-api:latest
```

说明：
- 你主程序数据库会在宿主机 `/root/newapi` 下（常见文件是 `one-api.db`）
- 如果你的数据库文件是 `/root/newapi/one-api.db`，就是这个映射方式

### 2) api-web-tgbot 一键命令（数据放 /root/newapi/web）
Docker Hub 镜像（已发布）：

```bash
docker run -d \
  --name api-web-tgbot \
  --restart unless-stopped \
  -p 8088:8088 \
  -e DATA_DIR=/data/api-web-tgbot \
  -e PORT=8088 \
  -v /root/newapi/web:/data/api-web-tgbot \
  -v /root/newapi:/data/newapi:ro \
  nbdsn/api-web-tgbot:latest
```

目录映射说明（非常重要）：
- 容器内 `/data/api-web-tgbot`：本程序的数据目录（`manager.db`、自动额度处理日志、后台配置等）
- 宿主机 `/root/newapi/web`：本程序数据持久化目录
- 这个映射只保存“本程序数据”，不会自动保存 NewAPI 主程序数据库
- 宿主机 `/root/newapi`：主程序数据库目录（只读挂载）
- 容器内 `/data/newapi`：在 Web 的“主程序数据库路径”里填写这里的路径
- `:ro` 为只读挂载，避免误写主程序文件

### 3) 开启数据库模式时怎么填
如果你主程序数据库在宿主机：
- `/root/newapi/one-api.db`

那在本程序 Web 后台里：
- 打开“数据库模式”
- 主程序数据库路径填写：`/data/newapi/one-api.db`

### 4) TG 反代怎么填
如果你有可反代 Telegram Bot API 的服务器域名（示例）：
- `https://tgapi.yourdomain.com`

那在本程序 TG 配置里：
- `TG API Base` 填：`https://tgapi.yourdomain.com`

要求：
- 必须启用 SSL（HTTPS）
- 反代需要支持 `/bot<token>/...` 路径转发到 Telegram 官方 Bot API

GHCR 镜像（可选）：

```bash
docker run -d \
  --name api-web-tgbot \
  --restart unless-stopped \
  -p 8088:8088 \
  -e DATA_DIR=/data/api-web-tgbot \
  -e PORT=8088 \
  -v /data/api-web-tgbot:/data/api-web-tgbot \
  ghcr.io/nbdsn/api-web-tgbot:latest
```

### 方式 B：本地构建

```bash
docker compose up -d --build
```

## Docker Hub 说明
- 已支持在 GitHub Actions 中“可选推送 Docker Hub”（需你在仓库 Secrets 配置）：
  - `DOCKERHUB_USERNAME`
  - `DOCKERHUB_TOKEN`
- 配置后会自动推送：
  - `<dockerhub用户名>/api-web-tgbot:latest`
  - `<dockerhub用户名>/api-web-tgbot:sha-...`

## 离线包发布（给国内机用）
在本地打包：

```bash
bash scripts/make_offline_package.sh
```

生成文件在 `dist/`，然后上传到 GitHub Release，建议额外上传一份同文件内容的别名：

```text
api-web-tgbot-offline-latest.tar.gz
```

这样国内机安装命令可长期固定不变。
