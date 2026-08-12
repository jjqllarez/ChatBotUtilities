# Despliegue en VPS (Ubuntu) — guía para el agente / manual

Este documento describe cómo instalar el bot en un VPS **Ubuntu**. Puede ejecutarlo
un agente (p. ej. Hermes) o seguirlo a mano.

## Requisitos del VPS
- Ubuntu (20.04/22.04/24.04) con `sudo`.
- RAM: **>= 1 GB** (recomendado 1-2 GB). Base ~30-80 MB; Chromium solo +aparece-~200-400 MB de forma transitoria al renderizar.
- Acceso de **salida HTTPS** (443/80) a WhatsApp, Supabase y OpenRouter.
- No necesita Node (usa Chrome CLI).

## Prerequisitos del sistema (una vez)

```bash
sudo apt update
sudo apt install -y chromium-browser fonts-liberation git

# Si el VPS no tiene Go y vamos a compilar ahí:
sudo apt install -y golang
```
> Si prefieres subir el binario ya compilado (desde Windows: `GOOS=linux GOARCH=amd64 go build -o bot .`),
> no necesitas instalar Go en el VPS.

## Pasos de instalación

```bash
# 1) Clonar
sudo mkdir -p /opt/whatsbot
sudo chown "$USER":"$USER" /opt/whatsbot
git clone <REPO_URL> /opt/whatsbot/repo

# 2) Compilar
cd /opt/whatsbot/repo
make build            # o: ./build.sh

# 3) Colocar binario + assets juntos
mkdir -p /opt/whatsbot
cp bot /opt/whatsbot/
cp -r assets /opt/whatsbot/

# 4) Crear .env a partir del ejemplo
cp .env.example /opt/whatsbot/.env
nano /opt/whatsbot/.env     # RELLENAR: SUPABASE_URL, SUPABASE_SERVICE_ROLE_KEY, OPENROUTER_API_KEY, CHROME_PATH, etc.
```

### Indicar el Chromium
```bash
which chromium-browser   # p. ej. /usr/bin/chromium-browser
# Añadir en /opt/whatsbot/.env:
# CHROME_PATH=/usr/bin/chromium-browser
```
El código usa `--no-sandbox`, necesario en servidores.

## Servicio systemd (siempre activo + reinicio)

```bash
sudo cp /opt/whatsbot/repo/deploy/whatsbot.service /etc/systemd/system/whatsbot.service
sudo systemctl daemon-reload
sudo systemctl enable --now whatsbot
# Ver logs:
journalctl -u whatsbot -f
```

El `.service` asume carpetas `/opt/whatsbot/` y `.env` ahí dentro (`EnvironmentFile=/opt/whatsbot/.env`).
Ajusta `User=`/`Group=` si quieres otro usuario que no sea `www-data`.

## Primera vinculación (QR)
La primera vez no hay sesión: el bot muestra un QR en los logs.
```bash
journalctl -u whatsbot -f    # o, si corres en terminal: tmux + ./bot
```
Escanéalo con WhatsApp → Dispositivos vinculados. La sesión se guarda en Supabase
(`whatsmeow_device`) y en reinicios posteriores **no vuelve a pedir QR**.

> Opcional: si quieres el QR como imagen en el navegador, expón el puerto
> (`QR_PORT`, default 8080) y abre `http://<VPS-IP>:8080/qr` desde un teléfono en la misma red.

## Secretos
- **Nunca** subir `.env` real al repo (`.gitignore` lo excluye). Solo `.env.example`.
- Si cambias el `.env`, reinicia: `sudo systemctl restart whatsbot`.

## Actualización
```bash
cd /opt/whatsbot/repo
git pull
make build
sudo systemctl restart whatsbot
```
