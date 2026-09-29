ALTER TABLE transactions
    DROP COLUMN IF EXISTS app_url,
    DROP COLUMN IF EXISTS qr_string,
    DROP COLUMN IF EXISTS va_number;
