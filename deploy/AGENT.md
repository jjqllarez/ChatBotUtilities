# AGENT.md — Consigna de despliegue para el agente

> Entregar esta consigna al agente (p. ej. Hermes) que operará sobre el VPS
> Ubuntu donde se instalará el bot. El agente debe:
> - Leer y seguir este archivo y `DEPLOY.md`.
> - No modificar el código salvo que algo no compile (y reportarlo).
> - No tocar `capital - cotizaciones` (no está en este repo ni debe usarse).

---

Eres el agente de despliegue. Instala y deja corriendo en este VPS **Ubuntu**
el bot de WhatsApp del repositorio `https://github.com/jjqllarez/ChatBotUtilities.git`
(rama `main`). Es un bot de **Go** (whatsmeow) que genera PDF/imagen de
cotizaciones usando **Chromium**.

## Pasos

```bash
# 1) Prerequisitos del sistema
sudo apt update
sudo apt install -y chromium-browser fonts-liberation git golang

# 2) Preparar carpeta
sudo mkdir -p /opt/whatsbot
sudo chown "$USER":"$USER" /opt/whatsbot

# 3) Clonar
git clone https://github.com/jjqllarez/ChatBotUtilities.git /opt/whatsbot/repo
cd /opt/whatsbot/repo
make build        # o: ./build.sh   (compila ./bot)

# 4) Colocar binario + assets juntos
cp bot /opt/whatsbot/
cp -r assets /opt/whatsbot/

# 5) Crear .env desde el ejemplo (NO vacío; sustituir valores reales)
cp .env.example /opt/whatsbot/.env
#    Rellenar (sin dejar secretos en blanco):
#      SUPABASE_URL=
#      SUPABASE_SERVICE_ROLE_KEY=
#      SUPABASE_ANON_KEY=
#      OPENROUTER_API_KEY=
#      CHROME_PATH=   <- lo que devuelve:  which chromium-browser
```

Registrar el servicio systemd:

```bash
sudo cp /opt/whatsbot/repo/deploy/whatsbot.service /etc/systemd/system/whatsbot.service
sudo systemctl daemon-reload
sudo systemctl enable --now whatsbot
journalctl -u whatsbot -f
```

## Consideraciones

- **Primera ejecución**: aparece un **QR** en los logs; hay que escanearlo con
  WhatsApp → Dispositivos vinculados. La sesión se guarda en Supabase y no vuelve
  a pedirse en reinicios.
- Ajusta `User=`/`Group=` del `.service` (por defecto `www-data`) y da permiso al
  usuario del servicio sobre `/opt/whatsbot/.env` (p. ej. `chown`).
- No uses ni subas el `.env` real al repo.
- RAM mínima recomendada: 1 GB.

## Al terminar

Reportar: estado de `systemctl status whatsbot`, si quedó conectado/vinculado, y
cualquier error que aparezca en `journalctl -u whatsbot`.
