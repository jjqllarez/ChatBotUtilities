-- Estado persistente de la conversación del bot (por número de teléfono).
-- Guarda el contexto/draft en curso (datos ya recopilados de una cotización)
-- para que el asistente sepa siempre en qué punto va y no se pierda
-- aunque el proceso se reinicie.

CREATE TABLE IF NOT EXISTS public.bot_chat_state (
    phone      text PRIMARY KEY,
    state      jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.bot_chat_state TO anon, authenticated, service_role;
