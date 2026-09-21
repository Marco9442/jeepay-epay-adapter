#!/usr/bin/env python3
"""对真实 Jeepay 支付网关做一次统一下单探测（无微信证书时期望渠道失败）。"""
from __future__ import annotations

import hashlib
import json
import os
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("JEEPAY_BASE_URL", "http://127.0.0.1:19218").rstrip("/")
MCH = os.environ.get("JEEPAY_MCH_NO", "M1680000001")
APP = os.environ.get("JEEPAY_APP_ID", "60cc09bce4b0f1c0b83761c9")
SECRET = os.environ.get("JEEPAY_APP_SECRET", "jeepay-dev-app-secret")


def sign(params: dict[str, str], key: str) -> str:
    items = [(k, v) for k, v in params.items() if k != "sign" and v != ""]
    items.sort(key=lambda kv: kv[0].lower())
    raw = "".join(f"{k}={v}&" for k, v in items) + "key=" + key
    return hashlib.md5(raw.encode()).hexdigest().upper()


def main() -> int:
    params = {
        "mchNo": MCH,
        "appId": APP,
        "mchOrderNo": "SMOKE" + str(int(time.time())),
        "wayCode": "WX_NATIVE",
        "amount": "100",
        "currency": "cny",
        "subject": "探测",
        "body": "探测",
        "notifyUrl": "http://127.0.0.1/unused",
        "clientIp": "127.0.0.1",
        "reqTime": str(int(time.time() * 1000)),
        "version": "1.0",
        "signType": "MD5",
        "channelExtra": '{"payDataType":"codeUrl"}',
    }
    params["sign"] = sign(params, SECRET)
    body = dict(params)
    body["amount"] = 100
    req = urllib.request.Request(
        BASE + "/api/pay/unifiedOrder",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            raw = resp.read().decode()
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        print("HTTP", e.code, raw[:500])
        return 1
    except Exception as e:  # noqa: BLE001
        print("请求失败:", e)
        return 1
    print(raw[:800])
    try:
        obj = json.loads(raw)
    except json.JSONDecodeError:
        return 1
    if obj.get("code") == 0:
        print("Jeepay 统一下单成功（渠道可用）")
        return 0
    print("Jeepay 已应答业务错误（无微信证书时这是预期）：", obj.get("msg"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
