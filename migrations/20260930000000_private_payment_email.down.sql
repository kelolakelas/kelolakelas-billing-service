ALTER TABLE transactions
    DROP COLUMN private_payment_email_failure_reason,
    DROP COLUMN private_payment_email_sent_at,
    DROP COLUMN private_payment_email_claimed_at,
    DROP COLUMN private_schedule_request;
