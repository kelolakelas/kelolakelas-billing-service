ALTER TABLE transactions DROP COLUMN IF EXISTS class_name;
ALTER TABLE transactions DROP COLUMN IF EXISTS paid_email_sent_at;
ALTER TABLE transactions DROP COLUMN IF EXISTS failed_email_sent_at;
