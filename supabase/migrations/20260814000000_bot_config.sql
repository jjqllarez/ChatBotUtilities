-- Configuración operativa del bot (key/value) leída en caliente desde Supabase.
-- Uso principal: bot_config.simulation = 'true'/'false' activa el MODO SIMULACIÓN,
-- en el que el bot procesa los mensajes por el pipeline completo (ruteo, flujos,
-- LLM, Supabase, PDF) pero NO envía nada por WhatsApp: registra en consola lo que
-- habría enviado y guarda los adjuntos en sim_out/. Útil para probar lógica de
-- ruteo y negocio sin riesgo de baneo por ráfagas de mensajes de prueba.

CREATE TABLE IF NOT EXISTS public.bot_config (
    key        text PRIMARY KEY,
    value      text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO public.bot_config (key, value)
VALUES ('simulation', 'false')
ON CONFLICT (key) DO NOTHING;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.bot_config TO anon, authenticated, service_role;
