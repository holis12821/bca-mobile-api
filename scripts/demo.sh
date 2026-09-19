#!/usr/bin/env bash
#
# BCA Mobile API — Demo Script
# Runs through key API flows, reports PASS/FAIL per step.
#
# Usage:
#   ./scripts/demo.sh [BASE_URL]
#
# Defaults to http://localhost:8080/v1
#
set -euo pipefail

BASE="${1:-http://localhost:8080/v1}"
PASS=0
FAIL=0

green()  { printf "\033[32m%s\033[0m\n" "$*"; }
red()    { printf "\033[31m%s\033[0m\n" "$*"; }
yellow() { printf "\033[33m%s\033[0m\n" "$*"; }

check() {
  local step="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    green "  PASS: $step"
    PASS=$((PASS + 1))
  else
    red "  FAIL: $step (expected $expected, got $actual)"
    FAIL=$((FAIL + 1))
  fi
}

check_contains() {
  local step="$1" needle="$2" haystack="$3"
  if echo "$haystack" | grep -q "$needle"; then
    green "  PASS: $step"
    PASS=$((PASS + 1))
  else
    red "  FAIL: $step (expected to contain '$needle')"
    FAIL=$((FAIL + 1))
  fi
}

echo ""
yellow "=== BCA Mobile API Demo ==="
yellow "Base URL: $BASE"
echo ""

# The API never accepts a plaintext PIN: it wants base64 of
# RSA-OAEP-SHA256({"pin","nonce","ts"}), single-use and valid for 60 seconds.
# /dev/encrypt-pin produces one, and exists only when APP_ENV=development.
encrypt_pin() {
  local pin="$1" resp
  resp=$(curl -s -X POST "$BASE/dev/encrypt-pin" \
    -H "Content-Type: application/json" \
    -d "{\"pin\": \"$pin\"}")

  local enc
  enc=$(echo "$resp" | jq -r '.data.pin_encrypted // empty' 2>/dev/null || echo "")
  if [ -z "$enc" ]; then
    red "  Cannot encrypt PIN. Is the server running with APP_ENV=development?"
    echo "  Response: $resp"
    exit 1
  fi
  echo "$enc"
}

# -----------------------------------------------------------
# 1. Health check
# -----------------------------------------------------------
yellow "1. Health check"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/health")
check "GET /health" "200" "$HTTP_CODE"

# -----------------------------------------------------------
# 2. Login with PIN (user: nurholis, PIN: 123456)
# -----------------------------------------------------------
yellow "2. Login with PIN"
LOGIN_RESP=$(curl -s -X POST "$BASE/auth/login/pin" \
  -H "Content-Type: application/json" \
  -d "{
    \"device_id\": \"device-nurholis-001\",
    \"pin_encrypted\": \"$(encrypt_pin 123456)\"
  }")
HTTP_CODE=$(echo "$LOGIN_RESP" | jq -r '.status // empty' 2>/dev/null || echo "error")
check "POST /auth/login/pin status" "success" "$HTTP_CODE"

ACCESS_TOKEN=$(echo "$LOGIN_RESP" | jq -r '.data.access_token // empty' 2>/dev/null || echo "")
if [ -z "$ACCESS_TOKEN" ]; then
  red "  Cannot proceed without access token. Aborting."
  echo ""
  echo "Results: $PASS passed, $FAIL failed"
  exit 1
fi
green "  Got access token"

AUTH="Authorization: Bearer $ACCESS_TOKEN"

# -----------------------------------------------------------
# 3. Get profile
# -----------------------------------------------------------
yellow "3. Get profile"
PROFILE_RESP=$(curl -s "$BASE/account/profile" -H "$AUTH")
check_contains "GET /account/profile" "NURHOLIS" "$PROFILE_RESP"

# -----------------------------------------------------------
# 4. Get balance
# -----------------------------------------------------------
yellow "4. Get balance"
BALANCE_RESP=$(curl -s "$BASE/account/balance" -H "$AUTH")
check_contains "GET /account/balance" "accounts" "$BALANCE_RESP"

# -----------------------------------------------------------
# 5. Get dashboard
# -----------------------------------------------------------
yellow "5. Get dashboard"
DASH_RESP=$(curl -s "$BASE/account/dashboard" -H "$AUTH")
check_contains "GET /account/dashboard" "success" "$DASH_RESP"

# -----------------------------------------------------------
# 6. List mutations
# -----------------------------------------------------------
yellow "6. List mutations"
ACCOUNT_ID="00000000-0000-0000-0000-100000000001"
MUT_RESP=$(curl -s "$BASE/transactions/mutations?account_id=$ACCOUNT_ID" -H "$AUTH")
check_contains "GET /transactions/mutations" "mutations" "$MUT_RESP"

# -----------------------------------------------------------
# 7. List transaction history
# -----------------------------------------------------------
yellow "7. List transaction history"
HIST_RESP=$(curl -s "$BASE/transactions/history" -H "$AUTH")
check_contains "GET /transactions/history" "transactions" "$HIST_RESP"

# -----------------------------------------------------------
# 8. List notifications
# -----------------------------------------------------------
yellow "8. List notifications"
NOTIF_RESP=$(curl -s "$BASE/notifications" -H "$AUTH")
check_contains "GET /notifications" "notifications" "$NOTIF_RESP"

# -----------------------------------------------------------
# 9. E-Wallet providers
# -----------------------------------------------------------
yellow "9. E-Wallet providers"
EWALLET_RESP=$(curl -s "$BASE/ewallet/providers" -H "$AUTH")
check_contains "GET /ewallet/providers" "providers" "$EWALLET_RESP"

# -----------------------------------------------------------
# 10. E-Wallet inquiry (stub)
# -----------------------------------------------------------
yellow "10. E-Wallet inquiry"
INQUIRY_RESP=$(curl -s -X POST "$BASE/ewallet/inquiry" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d "{
    \"provider_id\": \"gopay\",
    \"phone_number\": \"081234567890\",
    \"amount\": 50000,
    \"source_account_id\": \"$ACCOUNT_ID\"
  }")
check_contains "POST /ewallet/inquiry" "inquiry_id" "$INQUIRY_RESP"

# -----------------------------------------------------------
# 11. QRIS decode (EMVCo stub)
# -----------------------------------------------------------
yellow "11. QRIS decode"
# Minimal valid EMVCo TLV payload
QR_DATA="00020101021226250013ID.CO.BCA.WWW01041234520458125303360540850000.005802ID5914TOKO SEJAHTERA6007JAKARTA6304A13B"
QRIS_RESP=$(curl -s -X POST "$BASE/qris/decode" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d "{\"qr_data\": \"$QR_DATA\"}")
check_contains "POST /qris/decode" "merchant_name" "$QRIS_RESP"

# -----------------------------------------------------------
# 12. Login with wrong PIN (error path)
# -----------------------------------------------------------
yellow "12. Login with wrong PIN (error path)"
WRONG_RESP=$(curl -s -X POST "$BASE/auth/login/pin" \
  -H "Content-Type: application/json" \
  -d "{
    \"device_id\": \"device-nurholis-001\",
    \"pin_encrypted\": \"$(encrypt_pin 000000)\"
  }")
check_contains "POST /auth/login/pin (wrong)" "AUTH_INVALID_PIN" "$WRONG_RESP"

# -----------------------------------------------------------
# 13. E-Wallet provider down (phone ending 999)
# -----------------------------------------------------------
yellow "13. E-Wallet provider down (error path)"
DOWN_RESP=$(curl -s -X POST "$BASE/ewallet/inquiry" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d "{
    \"provider_id\": \"gopay\",
    \"phone_number\": \"081234567999\",
    \"amount\": 50000,
    \"source_account_id\": \"$ACCOUNT_ID\"
  }")
check_contains "POST /ewallet/inquiry (provider down)" "EWALLET_PROVIDER_DOWN" "$DOWN_RESP"

# -----------------------------------------------------------
# 14. Logout
# -----------------------------------------------------------
yellow "14. Logout"
LOGOUT_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/auth/logout" -H "$AUTH")
check "POST /auth/logout" "200" "$LOGOUT_CODE"

# -----------------------------------------------------------
# Summary
# -----------------------------------------------------------
echo ""
yellow "=== Results ==="
echo "  Passed: $PASS"
echo "  Failed: $FAIL"
echo "  Total:  $((PASS + FAIL))"

if [ "$FAIL" -gt 0 ]; then
  red "Some tests failed!"
  exit 1
else
  green "All tests passed!"
fi