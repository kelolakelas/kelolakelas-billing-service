CREATE UNIQUE INDEX IF NOT EXISTS uq_bank_accounts_tenant_primary
    ON bank_accounts (tenant_id)
    WHERE is_primary = true AND deleted_at IS NULL;
