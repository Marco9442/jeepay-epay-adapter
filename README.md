# jeepay-epay-adapter

独立的易支付门面，夹在 NewAPI 和 Jeepay 中间：NewAPI 只谈易支付，Jeepay 只谈商户 API 和微信，适配器做协议翻译。不改两端源码，也不直连微信支付。

```
NewAPI 钱包 → submit.php → 适配器收银台 / 微信扫码
            → Jeepay 统一下单 → 付款
            → Jeepay 通知适配器 → 适配器通知 NewAPI 入账
```

许可是 AGPL-3.0-or-later。合并进 `main` 会同时发 GitHub 和 CNB 两套镜像，源码相同，不必对 digest。

| 来源 | 镜像 |
| --- | --- |
| GHCR | `ghcr.io/marco9442/jeepay-epay-adapter:latest` |
| CNB | `docker.cnb.cool/baorui.xyz/jeepay-epay-adapter:latest` |

国内机器优先拉 CNB。生产流水线见 [docs/ci.md](docs/ci.md)。

## 生产

把镜像加到已有 NewAPI + Jeepay 堆栈中间，密钥放 `.env`，不要提交。

```bash
cp env.example .env
docker compose up -d
```

默认拉 GHCR。国内可改成：

```bash
ADAPTER_IMAGE=docker.cnb.cool/baorui.xyz/jeepay-epay-adapter:latest docker compose up -d
```

| 变量 | 含义 |
| --- | --- |
| `PUBLIC_BASE_URL` | 浏览器打开的适配器地址（收银台、回跳） |
| `INTERNAL_BASE_URL` | Jeepay 容器访问的适配器地址（异步通知） |
| `EPAY_PID` / `EPAY_KEY` | NewAPI 里填的易支付商户号和密钥 |
| `JEEPAY_BASE_URL` | Jeepay 支付网关，例如 `http://payment:9216` |
| `JEEPAY_MCH_NO` / `JEEPAY_APP_ID` / `JEEPAY_APP_SECRET` | Jeepay 商户应用 |
| `JEEPAY_WAY_CODE_WXPAY` | 默认 `WX_NATIVE` |

NewAPI：`PayAddress` 填浏览器能打开的适配器根地址，`EpayId` / `EpayKey` 与上面一致，`CustomCallbackAddress` 填适配器容器访问 NewAPI 的地址，支付方式含 `wxpay`。`Price=1` 时前端金额和实付元一致。

Jeepay：普通商户应用配好 `WX_NATIVE` 和微信证书。适配器会把 `notifyUrl` 设成 `{INTERNAL_BASE_URL}/jeepay/notify`。

订单在本地 SQLite（默认 `/data/adapter.db`）。入账通知失败会从 15 秒起重试，封顶 10 分钟，最多 10 次。`wxpay` 映射为 `WX_NATIVE`。金额用 NewAPI 提交的原始字符串，转 Jeepay 时换成整数分。

## 开发

两端都用 mock，用来打通协议：

```bash
docker compose -f compose.dev.yaml up --build
```

打开 http://127.0.0.1:13001 → 微信支付 → 扫码页点「确认付款」→ 余额增加。`./scripts/verify-dev.sh` 会走完这条链。

没有微信证书时，不要对真实 Jeepay 走 `WX_NATIVE`。用 `compose.integration.yaml` 接官方 NewAPI + mock Jeepay；`compose.jeepay.yaml` 只用来探真实 Jeepay 镜像能不能起来。
