-- KEL-143: withdrawal request + tenant cancel with atomic held balance.
--
-- withdrawals gains the columns the request/cancel flow needs:
--   idempotency_key  - client retry key scoped per tenant; NULL for rows written
--                      before KEL-143, which predate the retry contract.
--   bank_*_snapshot  - destination snapshot copied from the bank account at
--                      request time, so a later primary change or account edit
--                      never rewrites where an open request pays out.
--   cancelled_at     - when the tenant cancelled the request.
--
-- The partial unique index is the idempotency backstop: two concurrent inserts
-- with the same (tenant, key) serialize on it and exactly one wins. The
-- single-open-request rule is enforced by the usecase under the wallet row
-- lock, not by an index, because a partial unique index cannot express
-- "at most one row in (requested, processing)".
ALTER TABLE withdrawals
    ADD COLUMN IF NOT EXISTS idempotency_key varchar(255),
    ADD COLUMN IF NOT EXISTS bank_code_snapshot varchar(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS account_number_snapshot varchar(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS account_name_snapshot varchar(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS cancelled_at timestamp;

CREATE UNIQUE INDEX IF NOT EXISTS uq_withdrawals_tenant_idempotency
    ON withdrawals (tenant_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_withdrawals_tenant_status
    ON withdrawals (tenant_id, status);
