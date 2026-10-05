CREATE TABLE transaction_refunds (
    transaction_id UUID PRIMARY KEY REFERENCES transactions(id),
    tenant_id UUID NOT NULL,
    actor_id UUID NOT NULL,
    reason TEXT NOT NULL CHECK (length(trim(reason)) > 0),
    transfer_reference TEXT NOT NULL CHECK (length(trim(transfer_reference)) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    status VARCHAR(30) NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    last_error TEXT,
    completed_at TIMESTAMPTZ
);
