-- +notx
-- Long-lived traffic rollup for the reports page. peer_traffic_samples keeps only 7
-- days raw (migration 0011), so 30/90/365-day reports need a daily continuous
-- aggregate that outlives the raw rows. This file runs outside a transaction (see
-- noTxMarker in migrate.go) because TimescaleDB cannot create a continuous aggregate
-- or refresh one inside a transaction block; every statement is therefore idempotent.
--
-- materialized_only = false keeps real-time aggregation on: queries see the
-- materialised days plus not-yet-materialised raw rows, so "today" is never missing.
-- (Newer TimescaleDB versions default this to true, so it is set explicitly.)
CREATE MATERIALIZED VIEW IF NOT EXISTS traffic_daily
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket('1 day', ts) AS bucket,
       account_id,
       node_id,
       sum(rx_delta)::bigint AS rx_bytes,
       sum(tx_delta)::bigint AS tx_bytes
FROM peer_traffic_samples
GROUP BY 1, 2, 3
WITH NO DATA;

-- start_offset (3 days) stays inside the 7-day raw retention, so a refresh never
-- re-reads days whose raw rows were already dropped (which would zero them out).
SELECT add_continuous_aggregate_policy('traffic_daily',
    start_offset => INTERVAL '3 days',
    end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists => true);

SELECT add_retention_policy('traffic_daily', INTERVAL '400 days', if_not_exists => true);

CREATE INDEX IF NOT EXISTS idx_traffic_daily_account_bucket ON traffic_daily (account_id, bucket DESC);

-- Backfill whatever raw data exists now (at most 7 days) so reports are populated
-- immediately instead of after the first policy run.
CALL refresh_continuous_aggregate('traffic_daily', now() - INTERVAL '7 days', now());
