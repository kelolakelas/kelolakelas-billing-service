CREATE TABLE IF NOT EXISTS payment_reconciliations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id uuid NOT NULL UNIQUE REFERENCES transactions(id),
    enrollment_id uuid NOT NULL,
    status varchar(30) NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamp,
    last_attempt_at timestamp,
    last_error text,
    completed_at timestamp,
    created_at timestamp NOT NULL DEFAULT now(),
    updated_at timestamp NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_payment_reconciliations_due
    ON payment_reconciliations (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_payment_reconciliations_enrollment_id
    ON payment_reconciliations (enrollment_id);
