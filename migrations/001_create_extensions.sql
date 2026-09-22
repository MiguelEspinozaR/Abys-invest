-- 001_create_extensions.sql
-- Habilita TimescaleDB para las futuras hypertables (daily_prices, macro_series, M2).
--
-- Idempotente: CREATE EXTENSION ... IF NOT EXISTS.
--
-- Nota (desviación documentada, mitigación de riesgo del plan §7): en servidores
-- PostgreSQL puros sin la extensión instalada (entorno sin la imagen TimescaleDB),
-- se registra un NOTICE y la migración continúa. Las hypertables llegan en M2;
-- el esquema M1 es compatible con su incorporación posterior.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'timescaledb') THEN
        CREATE EXTENSION IF NOT EXISTS timescaledb;
        RAISE NOTICE 'timescaledb: extension creada';
    ELSE
        RAISE NOTICE 'timescaledb: extension no disponible en este servidor; continuando con PostgreSQL puro (fallback del plan §7)';
    END IF;
END
$$;