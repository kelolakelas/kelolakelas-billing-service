-- A single desired Academic state per subscription serializes suspend/resume calls.
-- An in-flight suspend remains processing when payment requests resume; the worker
-- completes that call before it can claim the pending resume.
CREATE TABLE subscription_lifecycle_reconciliations (
    subscription_id uuid PRIMARY KEY REFERENCES subscriptions(id),
    enrollment_id uuid NOT NULL,
    desired_action varchar(20) NOT NULL CHECK (desired_action IN ('suspend', 'resume')),
    status varchar(30) NOT NULL CHECK (status IN ('pending', 'processing', 'active', 'terminal_failed')),
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamp,
    last_attempt_at timestamp,
    last_error text,
    completed_at timestamp,
    updated_at timestamp NOT NULL DEFAULT now()
);
CREATE INDEX idx_subscription_lifecycle_due ON subscription_lifecycle_reconciliations(status, next_attempt_at);

-- Earlier expiry handling queued permanent seat releases for renewal invoices.
-- Withdraw only unclaimed jobs; an already processing/completed release is kept
-- as evidence and any later paid callback will surface a resume conflict.
DELETE FROM payment_reconciliations p
USING transactions t
WHERE p.transaction_id = t.id
  AND t.subscription_id IS NOT NULL AND t.billing_period_start IS NOT NULL
  AND p.kind = 'release' AND p.status = 'pending';
