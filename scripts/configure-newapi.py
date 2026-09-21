#!/usr/bin/env python3
"""给真实 NewAPI 写入易支付配置。默认账号 root / jeepay123（≥8 位）。"""
from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("NEWAPI_BASE", "http://127.0.0.1:13000").rstrip("/")
USER = os.environ.get("NEWAPI_USER", "root")
PASS = os.environ.get("NEWAPI_PASS", "jeepay123")
PAY_ADDRESS = os.environ.get("EPAY_PAY_ADDRESS", "http://127.0.0.1:18080")
EPAY_PID = os.environ.get("EPAY_PID", "1000")
EPAY_KEY = os.environ.get("EPAY_KEY", "dev-epay-key")
SERVER_ADDRESS = os.environ.get("NEWAPI_SERVER_ADDRESS", "http://127.0.0.1:13000")
CALLBACK = os.environ.get("NEWAPI_CALLBACK_ADDRESS", "http://new-api:3000")


class Client:
    def __init__(self) -> None:
        self.cj = urllib.request.HTTPCookieProcessor()
        self.op = urllib.request.build_opener(self.cj)
        self.token = ""

    def json(self, method: str, path: str, body: dict | None = None) -> dict:
        data = None if body is None else json.dumps(body).encode()
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        req = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
        try:
            with self.op.open(req, timeout=20) as resp:
                raw = resp.read().decode()
        except urllib.error.HTTPError as e:
            raw = e.read().decode()
            if e.code == 429:
                return {"success": False, "message": "rate_limited", "raw": raw[:200]}
            raise SystemExit(f"{method} {path} HTTP {e.code}: {raw[:400]}") from e
        try:
            return json.loads(raw)
        except json.JSONDecodeError as e:
            raise SystemExit(f"{method} {path} 非 JSON: {raw[:400]}") from e


def wait_ready(c: Client) -> None:
    last = ""
    for _ in range(60):
        try:
            with urllib.request.urlopen(BASE + "/api/status", timeout=5) as resp:
                if resp.status < 500:
                    return
        except Exception as e:  # noqa: BLE001
            last = str(e)
        time.sleep(2)
    raise SystemExit(f"NewAPI 未就绪: {last}")


def ensure_setup(c: Client) -> None:
    st = c.json("GET", "/api/setup")
    data = st.get("data") or {}
    if data.get("status"):
        return
    print("初始化 NewAPI root 账号")
    r = c.json(
        "POST",
        "/api/setup",
        {
            "username": USER,
            "password": PASS,
            "confirmPassword": PASS,
            "SelfUseModeEnabled": True,
            "DemoSiteEnabled": False,
        },
    )
    if not r.get("success"):
        raise SystemExit(f"初始化失败: {r}")
    print("初始化完成")


def main() -> int:
    c = Client()
    wait_ready(c)
    ensure_setup(c)
    # NewAPI 登录有内存限流；连打会 429。失败时重启容器即可清空计数。
    login = c.json("POST", "/api/user/login", {"username": USER, "password": PASS})
    if login.get("message") == "rate_limited":
        print("登录被限流。执行: docker restart jeepay-epay-int-new-api-1", file=sys.stderr)
        return 2
    if not login.get("success"):
        print("登录失败", login, file=sys.stderr)
        return 1
    data = login.get("data") or {}
    if isinstance(data, dict):
        c.token = str(data.get("access_token") or data.get("token") or "")
    print("已登录 NewAPI")

    comp = c.json("POST", "/api/option/payment_compliance", {"confirmed": True})
    print("合规确认", json.dumps(comp, ensure_ascii=False)[:240])

    options = {
        "ServerAddress": SERVER_ADDRESS,
        "PayAddress": PAY_ADDRESS,
        "EpayId": EPAY_PID,
        "EpayKey": EPAY_KEY,
        "MinTopUp": "1",
        "Price": "1",
        "PayMethods": json.dumps(
            [{"name": "微信", "type": "wxpay", "color": "rgba(var(--semi-green-5), 1)"}],
            ensure_ascii=False,
        ),
        "CustomCallbackAddress": CALLBACK,
    }
    for key, value in options.items():
        r = c.json("PUT", "/api/option/", {"key": key, "value": value})
        ok = r.get("success")
        print(("OK" if ok else "SKIP"), key, "" if ok else r.get("message", r))
        if key == "CustomCallbackAddress" and not ok:
            print("  当前版本可能没有 CustomCallbackAddress，异步通知必须让适配器容器访问到 NewAPI")
    print("完成。打开", SERVER_ADDRESS, f"用 {USER}/{PASS} 登录后去钱包充值。")
    print("异步通知基址 =", CALLBACK, "浏览器回跳基址 =", SERVER_ADDRESS)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
