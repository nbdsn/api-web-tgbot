# 国内部署指南（Gitee / 离线）

## 方案 A：Gitee 在线安装

适合服务器能访问 Gitee，但访问 GitHub 不稳定。

### 步骤 1：同步到 Gitee

把 `https://github.com/nbdsn/api-web-tgbot.git` 同步到你的 Gitee 仓库。

### 步骤 2：服务器执行

```bash
curl -fsSL https://gitee.com/<你的账号>/<你的仓库>/raw/main/scripts/install_from_gitee.sh -o /tmp/install_from_gitee.sh
GITEE_REPO_URL=https://gitee.com/<你的账号>/<你的仓库>.git sudo bash /tmp/install_from_gitee.sh
```

## 方案 B：离线安装

适合服务器无法访问外网。

### 步骤 1：本地打包

```bash
tar -czf api-web-tgbot.tar.gz .
```

### 步骤 2：上传服务器

```bash
scp -P <端口> api-web-tgbot.tar.gz root@<服务器IP>:/tmp/
```

### 步骤 3：服务器安装

```bash
mkdir -p /tmp/api-web-tgbot-src
cd /tmp/api-web-tgbot-src
tar -xzf /tmp/api-web-tgbot.tar.gz -C .
sudo bash scripts/install_offline.sh /tmp/api-web-tgbot-src
```

## 备注

- 安装菜单支持自定义安装目录、数据目录、端口（回车默认）
- 数据目录用于保存数据库和日志
- 卸载保留数据目录
