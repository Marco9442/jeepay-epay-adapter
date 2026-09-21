#!/usr/bin/env bash
# 开发堆栈冒烟：模拟 NewAPI 下单 → 适配器收银台 → 模拟微信确认付款 → 入账。
set -euo pipefail
BASE_NEWAPI="${BASE_NEWAPI:-http://127.0.0.1:13001}"
BASE_ADAPTER="${BASE_ADAPTER:-http://127.0.0.1:18080}"

curl -fsS "$BASE_NEWAPI/healthz" >/dev/null
curl -fsS "$BASE_ADAPTER/healthz" >/dev/null

html=$(curl -fsS -X POST "$BASE_NEWAPI/api/user/pay" --data "amount=10.00&payment_method=wxpay")

eval "$(python3 - "$html" <<'PY'
import re, sys, json
html = sys.argv[1] if len(sys.argv) > 1 else sys.stdin.read()
action = re.search(r"action=['\"]([^'\"]+)['\"]", html)
if not action:
    sys.exit("未能解析 submit.php")
pairs = re.findall(r'name="([^"]+)" value="([^"]*)"', html)
print("PAY_URL=" + json.dumps(action.group(1)))
print("FIELDS=" + json.dumps("&".join(f"{k}={v}" for k, v in pairs)))
PY
)"

loc=$(curl -sS -o /tmp/jeepay-epay-submit.body -D - -X POST "$PAY_URL" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data "$FIELDS" | awk 'tolower($1)=="location:"{print $2}' | tr -d '\r')
trade=$(printf '%s' "$loc" | sed -n 's#.*/pay/##p')
if [ -z "$trade" ]; then
  echo "没有拿到 trade_no，redirect=$loc" >&2
  cat /tmp/jeepay-epay-submit.body >&2
  exit 1
fi

page=$(curl -fsS "$BASE_ADAPTER/pay/$trade")
wx=$(printf '%s' "$page" | python3 -c "import sys,re; m=re.search(r'href=\"([^\"]+)\"', sys.stdin.read()); print(m.group(1) if m else '')")
if [ -z "$wx" ]; then
  echo "收银台没有支付链接" >&2
  exit 1
fi
# 不要用 curl -X POST -L：强制 POST 会把 302 后的 GET 收银台再 POST 一次，得到 405。
pay_url="$wx"
case "$pay_url" in
  */pay) ;;
  *) pay_url="${pay_url%/}/pay" ;;
esac
curl -fsS -o /dev/null -X POST "$pay_url"

st=""
for _ in $(seq 1 25); do
  st=$(curl -fsS "$BASE_ADAPTER/pay/$trade/status")
  echo "$st" | grep -q '"state":"paid"' && break
  sleep 1
done
echo "$st" | grep -q '"state":"paid"' || { echo "未支付成功: $st" >&2; exit 1; }

page=$(curl -fsS "$BASE_NEWAPI/usage-logs")
echo "$page" | grep -q "当前余额" || { echo "模拟 NewAPI 未返回余额页" >&2; echo "$page"; exit 1; }
echo "OK trade=$trade"
echo "$page" | python3 -c "import sys,re; m=re.search(r'当前余额：<b>([0-9.]+)</b>', sys.stdin.read()); print('balance', m.group(1) if m else '?')"
