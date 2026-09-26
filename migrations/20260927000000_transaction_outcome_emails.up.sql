ALTER TABLE transactions ADD COLUMN IF NOT EXISTS paid_email_sent_at timestamp;
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS failed_email_sent_at timestamp;
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS class_name varchar(255) NOT NULL DEFAULT '';
