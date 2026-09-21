#!/usr/bin/env python3
"""真实 NewAPI → 适配器 → mock-jeepay 主路径：下单、模拟付款、入账。"""
from __future__ import annotations

import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

NEWAPI = os.environ.get("NEWAPI_BASE", "http://127.0.0.1:13000").rstrip("/")
ADAPTER = os.environ.get("BASE_ADAPTER", "http://127.0.0.1:18080").rstrip("/")
USER = os.environ.get("NEWAPI_USER", "root")
PASS = os.environ.get("NEWAPI_PASS", "jeepay123")


class Client:
    def __init__(self) -> None:
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor())
        self.token = ""

    def json(self, method: str, path: str, body: dict | None = None) -> dict:
        data = None if body is None else json.dumps(body).encode()
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        req = urllib.request.Request(NEWAPI + path, data=data, method=method, headers=headers)
        try:
            with self.op.open(req, timeout=20) as resp:
                return json.loads(resp.read().decode())
        except urllib.error.HTTPError as e:
            raw = e.read().decode()
            if e.code == 429:
                return {"success": False, "message": "rate_limited", "raw": raw[:200]}
            raise SystemExit(f"{method} {path} HTTP {e.code}: {raw[:400]}") from e


def fetch(url: str, method: str = "GET", data: bytes | None = None, headers: dict | None = None, follow: bool = True):
    req = urllib.request.Request(url, data=data, method=method, headers=headers or {})
    opener = urllib.request.build_opener() if follow else urllib.request.build_opener(NoRedirect)
    return opener.open(req, timeout=20)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: ANN001
        return None


def main() -> int:
    c = Client()
    login = c.json("POST", "/api/user/login", {"username": USER, "password": PASS})
    if not login.get("success"):
        print("登录失败", login, file=sys.stderr)
        return 1
    data = login.get("data") or {}
    c.token = str(data.get("access_token") or data.get("token") or "")
    me = c.json("GET", "/api/user/self")
    before = ((me.get("data") or {}) if isinstance(me.get("data"), dict) else {}) .get("quota")
    print("登录成功 quota_before=", before)

    pay = c.json("POST", "/api/user/pay", {"amount": 10, "payment_method": "wxpay"})
    if pay.get("message") != "success":
        print("拉起支付失败", pay, file=sys.stderr)
        return 1
    params = pay.get("data") or {}
    url = pay.get("url") or (ADAPTER + "/submit.php")
    print("submit", url, "out_trade_no=", params.get("out_trade_no"))
    body = urllib.parse.urlencode(params).encode()
    try:
        resp = fetch(url, "POST", body, {"Content-Type": "application/x-www-form-urlencoded"}, follow=False)
        loc = resp.headers.get("Location")
    except urllib.error.HTTPError as e:
        loc = e.headers.get("Location")
        if e.code not in (301, 302, 303, 307, 308) or not loc:
            print("submit.php HTTP", e.code, e.read()[:300], file=sys.stderr)
            return 1
    if not loc:
        print("submit.php 未跳转收银台", file=sys.stderr)
        return 1
    if loc.startswith("/"):
        loc = ADAPTER + loc
    m = re.search(r"/pay/([^/?]+)", loc)
    if not m:
        print("无法解析 trade", loc, file=sys.stderr)
        return 1
    trade = m.group(1)
    page = fetch(loc).read().decode()
    href = re.search(r'href="([^"]+)"', page)
    if not href:
        print("收银台无支付链接", file=sys.stderr)
        return 1
    wx = href.group(1).rstrip("/") + "/pay"
    print("模拟付款", wx)
    try:
        fetch(wx, "POST", follow=True).read()
    except urllib.error.HTTPError as e:
        if e.code not in (301, 302, 303, 404):
            print("模拟付款 HTTP", e.code, file=sys.stderr)
            return 1

    paid = False
    for _ in range(30):
        st = json.loads(fetch(ADAPTER + "/pay/" + trade + "/status").read().decode())
        if st.get("state") == "paid":
            paid = True
            break
        time.sleep(1)
    if not paid:
        print("适配器未标记已支付", file=sys.stderr)
        return 1
    print("适配器已支付")

    after = before
    for _ in range(30):
        me = c.json("GET", "/api/user/self")
        after = ((me.get("data") or {}) if isinstance(me.get("data"), dict) else {}).get("quota")
        if before is None or after != before:
            break
        time.sleep(1)
    print("quota_after=", after)
    if after == before:
        print("NewAPI 额度未增加", file=sys.stderr)
        return 1
    print("OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
