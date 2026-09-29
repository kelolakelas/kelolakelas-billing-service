ALTER TABLE transactions
    ADD COLUMN private_schedule_request boolean NOT NULL DEFAULT false,
    ADD COLUMN private_payment_email_claimed_at timestamp,
    ADD COLUMN private_payment_email_sent_at timestamp,
    ADD COLUMN private_payment_email_failure_reason text;
