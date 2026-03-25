# 安装与运维说明

## 一键安装命令

```bash
sudo bash <(curl -fsSL https://raw.githubusercontent.com/nbdsn/api-web-tgbot/main/scripts/install_from_github.sh)
```

## 离线安装命令

```bash
curl -fL https://github.com/nbdsn/api-web-tgbot/releases/latest/download/api-web-tgbot-offline-latest.tar.gz -o /tmp/api-web-tgbot-offline-latest.tar.gz
mkdir -p /tmp/api-web-tgbot-offline
tar -xzf /tmp/api-web-tgbot-offline-latest.tar.gz -C /tmp/api-web-tgbot-offline
sudo bash /tmp/api-web-tgbot-offline/scripts/jdc_manager.sh menu /tmp/api-web-tgbot-offline
```

## 交互安装参数

脚本会提示：

- 安装目录（默认 `/opt/api-web-tgbot`）
- 数据目录（默认 `/data/api-web-tgbot`，数据库和日志都在这里）
- 服务端口（默认 `8088`）

## 管理菜单能力

- 启动服务
- 停止服务
- 重启服务
- 查看状态
- 备份数据库
- 还原数据库（latest 或指定路径）
- 清空数据库（恢复到初始账号）
- 卸载程序（保留数据目录和日志）

## systemd 服务名

`api-web-tgbot.service`

常用命令：

```bash
systemctl status api-web-tgbot.service
journalctl -u api-web-tgbot.service -f
```
