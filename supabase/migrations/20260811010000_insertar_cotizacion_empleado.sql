-- RPC puente para que el bot (service role) pueda insertar cotizaciones
-- como un empleado concreto, reutilizando insertar_cotizacion y el trigger
-- auto_fill_cotizacion_fields sin modificar nada de lo existente.
--
-- Es SECURITY DEFINER: corre como el owner. Antes de insertar, inyecta el
-- user_id del empleado en el contexto del JWT (request.jwt.claim.sub) para que
-- auth.uid() devuelva ese uid y el trigger rellene empleado_id/socio_comercial_id.

CREATE OR REPLACE FUNCTION public.insertar_cotizacion_empleado(
    p_user_id uuid,
    p_cliente_id bigint,
    p_version_id bigint,
    p_numero_presupuesto character varying,
    p_forma_pago character varying,
    p_precio_vehiculo numeric,
    p_estado character varying,
    p_inf_opcional jsonb,
    p_detalle_cotizacion jsonb
)
RETURNS TABLE(id bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $function$
BEGIN
    PERFORM set_config('request.jwt.claim.sub', p_user_id::text, true);
    PERFORM set_config('request.jwt.claim.role', 'authenticated', true);

    RETURN QUERY
    SELECT q.id
    FROM public.insertar_cotizacion(
        p_cliente_id,
        p_version_id,
        p_numero_presupuesto,
        p_forma_pago,
        p_precio_vehiculo,
        p_estado,
        p_inf_opcional,
        p_detalle_cotizacion
    ) q;
END;
$function$;

GRANT EXECUTE ON FUNCTION public.insertar_cotizacion_empleado(uuid, bigint, bigint, character varying, character varying, numeric, character varying, jsonb, jsonb) TO anon, authenticated, service_role;