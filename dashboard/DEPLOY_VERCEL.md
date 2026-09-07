# 看板部署到 Vercel(探针留在内网)

架构:探针 / Prometheus / 聚合服务全部留在内网,不开任何入站端口;
内网机器上跑一个 **outbound 隧道** 把 Prometheus 的只读查询口送出去;
Vercel 上的看板通过 **服务端 API route** 走隧道取数,隧道地址与凭证只存在
Vercel 环境变量里,浏览器永远看不到内网入口。

```
.24 / .25 GPU 节点 ── DCGM-exporter ─┐
                                      ├─→ Prometheus(内网 :9090)
vLLM / 探针 aitra_* 指标 ────────────┘         │
                                        cloudflared(outbound)
                                               │
                              https://prom-xxx.example.com(带鉴权)
                                               │
                    Vercel 看板 API route(PROMETHEUS_URL 指向隧道)
                                               │
                                         浏览器(公网)
```

## 1. 内网侧:建隧道(以 Cloudflare Tunnel 为例)

在能访问 Prometheus 的内网机器(如 .24)上:

```bash
# 安装 cloudflared 后
cloudflared tunnel login
cloudflared tunnel create aitra-prom
cloudflared tunnel route dns aitra-prom prom-aitra.<你的域名>
cat > ~/.cloudflared/config.yml <<EOF
tunnel: aitra-prom
credentials-file: /root/.cloudflared/<tunnel-id>.json
ingress:
  - hostname: prom-aitra.<你的域名>
    service: http://localhost:9090
  - service: http_status:404
EOF
cloudflared tunnel run aitra-prom   # 建议配 systemd 常驻
```

然后在 Cloudflare Zero Trust 控制台给该 hostname 加一条 **Access 策略**,
创建 Service Token,记下 Client ID / Client Secret —— 没有这一步等于把
Prometheus 裸露公网,不要跳过。

没有域名的话,备选:frp / Tailscale Funnel,或临时用
`cloudflared tunnel --url http://localhost:9090`(随机域名、无鉴权,只适合验证连通)。

## 2. Vercel 侧

1. 导入仓库,**Root Directory 选 `dashboard/`**,框架自动识别 Next.js。
2. 配环境变量(全部 Server-side,不要加 NEXT_PUBLIC_ 前缀):

| 变量 | 值 |
|---|---|
| `PROMETHEUS_URL` | `https://prom-aitra.<你的域名>` |
| `AGGREGATION_URL` | 聚合服务的隧道地址(没有可不设,chargeback 表会显示错误) |
| `CF_ACCESS_CLIENT_ID` / `CF_ACCESS_CLIENT_SECRET` | Access Service Token |
| `DASHBOARD_USER` / `DASHBOARD_PASSWORD` | 看板本身的 Basic Auth(建议设置) |

3. Deploy。完成后浏览器访问 Vercel 域名,输入 Basic Auth 即见看板。

## 3. 安全边界(代码已内置)

- `/api/metrics` 与 `/api/metrics/range` 有 **PromQL 白名单守卫**
  (`lib/promguard.ts`):只放行 `aitra_*` 指标 + 固定的函数/标签集,
  公网上的看板不能被当成任意 PromQL 网关去翻整个 Prometheus。
- 隧道凭证通过 `lib/upstream.ts` 只在服务端注入,支持 Bearer Token
  (`PROMETHEUS_BEARER_TOKEN`)或 Cloudflare Access Service Token 两种。
- 设了 `DASHBOARD_PASSWORD` 时 `middleware.ts` 对**所有**页面和 API 生效。

## 4. 监控多台 GPU 服务器(如 .24 + .25)

看板不感知节点数量——它只读 Prometheus。新增节点只需:

1. 新节点装 DCGM-exporter;
2. Prometheus 加 scrape target;
3. 探针 `--deployments` 里补上该节点的部署条目(`node` 字段区分)。

看板的表格与图会随 `aitra_*` 序列自动多出对应行/线。
