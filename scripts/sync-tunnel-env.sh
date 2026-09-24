#!/usr/bin/env bash
# Sinkronkan SIGNALING_BASE_URL di .env dengan tunnel ngrok yang sedang jalan.
#
# Endpoint video call menyerahkan signaling_url ke klien. Kalau nilainya masih
# ws://localhost:8080, HP tester tidak akan pernah bisa menyambung — localhost
# di HP adalah HP itu sendiri. Nilainya juga harus wss:// (bukan ws://) karena
# ngrok menutup koneksi dengan TLS, dan config.Validate menolak start di luar
# development kalau skemanya bukan wss.
#
# ngrok mengekspos agent API di 127.0.0.1:4040, jadi URL-nya bisa dibaca
# langsung tanpa copy-paste manual yang gampang salah.
set -euo pipefail

ENV_FILE="${ENV_FILE:-.env}"
API="${NGROK_API:-http://127.0.0.1:4040/api/tunnels}"

if ! curl -sf --max-time 3 "$API" >/dev/null 2>&1; then
  echo "ngrok tidak terdeteksi di 127.0.0.1:4040." >&2
  echo "Jalankan 'make tunnel' di terminal lain lebih dulu." >&2
  exit 1
fi

PUBLIC_URL=$(curl -s --max-time 3 "$API" \
  | python3 -c 'import sys,json; t=json.load(sys.stdin)["tunnels"]; print(next((x["public_url"] for x in t if x["public_url"].startswith("https://")), ""))')

if [ -z "$PUBLIC_URL" ]; then
  echo "Tunnel HTTPS belum siap. Coba lagi beberapa detik." >&2
  exit 1
fi

HOST="${PUBLIC_URL#https://}"
WSS="wss://${HOST}"

if [ ! -f "$ENV_FILE" ]; then
  echo "$ENV_FILE tidak ada. Jalankan 'make setup' dulu." >&2
  exit 1
fi

# Ganti baris yang ada, atau tambahkan kalau belum ada.
if grep -q '^SIGNALING_BASE_URL=' "$ENV_FILE"; then
  # -i.bak dipakai supaya portable antara BSD sed (macOS) dan GNU sed.
  sed -i.bak "s|^SIGNALING_BASE_URL=.*|SIGNALING_BASE_URL=${WSS}|" "$ENV_FILE"
  rm -f "${ENV_FILE}.bak"
else
  printf '\nSIGNALING_BASE_URL=%s\n' "$WSS" >> "$ENV_FILE"
fi

echo "URL publik          : ${PUBLIC_URL}"
echo "SIGNALING_BASE_URL  : ${WSS}"
echo
echo "base_url Postman    : ${PUBLIC_URL}/v1"
echo
echo "'air' akan me-restart server otomatis karena .env berubah."
echo "Kalau memakai 'make run', hentikan dan jalankan ulang."
