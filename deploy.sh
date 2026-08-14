#!/bin/bash
# deploy.sh - build + desplegar + reiniciar el bot de WhatsApp (Capital Motors).
# Uso: sudo ./deploy.sh
set -euo pipefail

REPO="/home/adminvps/services/ChatBotUtilities"
BIN="/opt/whatsbot/bot"
SVC="whatsbot"

cd "$REPO"

echo "==> Build"
./build.sh

echo "==> Copiar binario a $BIN"
sudo cp "$REPO/bot" "$BIN.new"
sudo chown www-data:www-data "$BIN.new"
sudo mv -f "$BIN.new" "$BIN"
sudo cp -r "$REPO/assets/." "/opt/whatsbot/assets/"
sudo chown -R www-data:www-data "/opt/whatsbot/assets"

echo "==> Reiniciar $SVC"
sudo systemctl restart "$SVC"
sleep 5
if systemctl is-active --quiet "$SVC"; then
  echo "OK: $SVC activo"
else
  echo "ERROR: $SVC no arrancó"
  sudo journalctl -u "$SVC" -n 30 --no-pager
  exit 1
fi
