# 前置 nginx

把控制台放到 HTTPS 后面。**读之前先确认你要的是这个**——
[控制台部署](控制台部署.md)默认走 SSH 隧道，不需要 nginx。

前置反代**换掉了 [ADR-0013](../docs/adr/0013-console-access-is-network-plus-session.md)
准入的第一条**：原来是「只绑内网」，之后变成「nginx 上的 TLS 配置 +
你在 nginx 上加的任何访问控制」。**那一条的强度从此取决于下面这个文件，
而不再取决于网络位置。**

前提：控制台已经按[那份文档](控制台部署.md)跑起来了，`systemctl status edge-master`
是 running。

---

## 1. 装 nginx

```bash
# Debian / Ubuntu
sudo apt-get install -y nginx

# RHEL / Rocky / Alma
sudo dnf install -y nginx
```

装完它自带 systemd 单元，**不需要自己写**。

---

## 2. 先拿到证书

**证书要在配置生效之前就位**——配置里写着证书路径，文件不在 nginx 起不来。

```bash
# Debian / Ubuntu
sudo apt-get install -y certbot python3-certbot-nginx
# RHEL 系
sudo dnf install -y certbot python3-certbot-nginx

sudo certbot certonly --nginx -d console.example.com
```

> **不要用本系统签的证书。** 那两套 CA（隧道 + 回源）是内部 PKI，
> 浏览器不认它们——现象是每次打开控制台都要点「继续访问」，
> 而那个提示会训练你忽略证书警告。

### 续期时要 reload nginx

certbot 装的定时任务会续证书，**但不会让 nginx 重新读它**。
不加这一步的话，证书在第 60 天续上了，而 nginx 手里还是旧的那份，
到第 90 天过期——**中间那 30 天什么症状都没有**。

```bash
sudo mkdir -p /etc/letsencrypt/renewal-hooks/deploy
sudo tee /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh >/dev/null <<'EOF'
#!/bin/sh
systemctl reload nginx
EOF
sudo chmod +x /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh
```

---

## 3. 放配置

```bash
# Debian / Ubuntu
sudo cp nginx-console.conf /etc/nginx/sites-available/edge-console.conf
sudo ln -sf /etc/nginx/sites-available/edge-console.conf /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default        # ← 别忘了

# RHEL 系
sudo cp nginx-console.conf /etc/nginx/conf.d/edge-console.conf
```

把配置里的 `console.example.com` 换成你的域名（三处：两个 `server_name`
和证书路径）。

**Debian 系那条 `rm default` 不是可选的**：默认站点也监听 80 和 443，
两个 `server` 块抢同一个端口时 nginx 用**先加载的那个**——而 `default`
按字母序排在前面。现象是打开域名看到 nginx 欢迎页，而配置文件看起来完全正确。

### 检查语法

```bash
sudo nginx -t
```

**这一步不能跳。** 配置有错时 `systemctl reload` 会**保持旧配置继续跑**
并返回成功——你会以为改生效了。

---

## 4. RHEL 系：SELinux 要放行

Debian / Ubuntu 跳过这一步。

```bash
sudo setsebool -P httpd_can_network_connect 1
```

不放行的话 nginx 连不上 127.0.0.1:8080，**浏览器看到 502**，
而 nginx 的 error log 里是 `Permission denied`——**那个错误看起来像文件权限，
而它是 SELinux**。

---

## 5. 主控要多配两个变量

```bash
sudo tee -a /etc/edge-master.env >/dev/null <<'EOF'
EC_SECURE_COOKIE=1
EC_TRUSTED_PROXIES=127.0.0.1
EOF

sudo systemctl restart edge-master
```

| 漏了会怎样 |
|---|
| **`EC_SECURE_COOKIE`** — 会话 Cookie 不带 `Secure` 标志。**它仍然能用**，所以这个疏漏一点症状都没有 |
| **`EC_TRUSTED_PROXIES`** — 审计日志里的来源 IP **全部**变成 `127.0.0.1`（nginx 自己） |

> **主控默认「谁也不信」是有理由的。** gin 的默认是信任所有代理，
> 那时任何能访问控制台的人发一个 `X-Forwarded-For: 1.2.3.4` 就能让审计
> 记下那个 IP——**而审计是这套准入模型的三分之一**。

---

## 6. 起 nginx

```bash
sudo systemctl enable --now nginx
sudo systemctl reload nginx      # 已经在跑的话用这个
```

**两个服务之间不需要 `After=`。** nginx 先起会有一小段 502，主控先起会有
一小段连不上——两种都会自愈，加依赖反而会让其中一个的重启拖住另一个。

---

## 7. 验证

**下面四条要一条条过。** 前三条对应的三种故障**都不会有明显症状**——
控制台照样能打开、能登录、能点。

### 7.1 HTTP 跳转与证书

```bash
curl -sI http://console.example.com | head -2
# 期待：HTTP/1.1 301 ... Location: https://console.example.com/

curl -sI https://console.example.com | head -1
# 期待：HTTP/2 200
```

### 7.2 WebSocket 真的升级了

```bash
curl -sI -o /dev/null -w '%{http_code}\n' \
  -H "Connection: Upgrade" -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
  https://console.example.com/api/v1/ws
```

**期待 `401`**（没登录）。这个数字是实测出来的，不是推的。

- `401` = 请求**到达了主控的鉴权**，升级路径是通的
- `200` 或 HTML = nginx 把它当普通请求转过去了，那一段没生效

> **路径是 `/api/v1/ws`，不是 `/ws`。** 我第一版配置就写错成 `/ws` ——
> 那一段匹配不到任何东西，请求会掉进 `location /`，
> 而那里有一句 `proxy_set_header Connection ""` 把升级头**主动清掉**。
>
> 写错的后果和漏写完全一样，而且更难查：配置文件里明明有一段
> WebSocket 的处理，看起来一切正常。

> 漏了 `/ws` 那段的表现是**「页面能开、数字不动」**——看起来像后端没在推，
> 而实际上是 nginx 没让它升级。前端会无声地降级成 2 秒轮询，
> 界面照常更新，只是慢了。

### 7.3 审计里的来源 IP 是你，不是 nginx

**从另一台机器**（不是主控自己）用浏览器登录一次，然后：

```bash
curl -s -b cookies.txt https://console.example.com/api/v1/audit | head -c 400
```

或者直接看控制台的审计页。**`src_ip` 应当是你那台机器的地址**。

如果它是 `127.0.0.1`，说明 `EC_TRUSTED_PROXIES` 没生效——
主控在把 nginx 自己的地址当成来源。

### 7.4 Cookie 带了 Secure

浏览器开发者工具 → Application → Cookies，看 `ec_session` 那一行的
**Secure** 列有没有勾。

没勾说明 `EC_SECURE_COOKIE` 没生效。**它不影响使用**，所以只能这样看。

---

## 出了问题看哪里

```bash
sudo journalctl -u nginx -n 30 --no-pager        # nginx 起不来
sudo tail -50 /var/log/nginx/error.log           # 502 / 403 看这里
sudo journalctl -u edge-master -n 30 --no-pager  # 主控自己的问题
```

| 现象 | 多半是 |
|---|---|
| 打开域名看到 nginx 欢迎页 | Debian 系没删 `sites-enabled/default` |
| 502 Bad Gateway | 主控没跑；或 RHEL 系没开 `httpd_can_network_connect` |
| 登录成功但立刻跳回登录页 | 从 `http://` 进的——Secure Cookie 不在 HTTP 上发送 |
| 页面能开、数字不动 | `/ws` 那段没生效，或 `proxy_read_timeout` 太短 |
| 改了配置没变化 | `reload` 时配置有错，nginx 保留了旧的那份。跑 `nginx -t` |

---

## 关掉它

反代不合适的话，回到 SSH 隧道：

```bash
sudo systemctl disable --now nginx
sudo sed -i '/EC_SECURE_COOKIE/d;/EC_TRUSTED_PROXIES/d' /etc/edge-master.env
sudo systemctl restart edge-master
```

**两个变量要一起去掉。** 留着 `EC_SECURE_COOKIE=1` 而没有 HTTPS 的话，
Cookie 在 `http://` 上不会被发送——现象是「登录成功但立刻跳回登录页」，
而那时你已经把 nginx 关了，不会往这边想。
