-- Historial de conversación del bot (memoria para el LLM)
-- Se persiste por número de teléfono para que el asistente recuerde
-- la conversación aunque el proceso se reinicie.

CREATE TABLE IF NOT EXISTS public.bot_chat_history (
    id         bigserial PRIMARY KEY,
    phone      text        NOT NULL,
    role       text        NOT NULL,
    content    text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_bot_chat_history_phone_created
    ON public.bot_chat_history (phone, created_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.bot_chat_history TO anon, authenticated, service_role;
GRANT USAGE, SELECT ON SEQUENCE public.bot_chat_history_id_seq TO anon, authenticated, service_role;
