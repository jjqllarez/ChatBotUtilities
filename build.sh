#!/usr/bin/env bash
# Compila el bot para la plataforma actual (Linux del VPS).
set -e
cd "$(dirname "$0")"
go build -o bot .
echo "Bot compilado: $(pwd)/bot"
