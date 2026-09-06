CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS vouchers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, code varchar(255) NOT NULL,
    discount_type varchar(255) NOT NULL, discount_value numeric(15,2) NOT NULL, max_discount_amount bigint,
    min_transaction_amount bigint NOT NULL DEFAULT 0, max_uses integer, current_uses integer NOT NULL DEFAULT 0,
    valid_from timestamp, valid_until timestamp, is_active boolean NOT NULL DEFAULT true,
    created_at timestamp NOT NULL DEFAULT now(), updated_at timestamp NOT NULL DEFAULT now(), deleted_at timestamp,
    CONSTRAINT uq_vouchers_tenant_code UNIQUE (tenant_id, code)
);
CREATE INDEX IF NOT EXISTS idx_vouchers_tenant_id ON vouchers (tenant_id);
CREATE INDEX IF NOT EXISTS idx_vouchers_is_active ON vouchers (is_active);
CREATE INDEX IF NOT EXISTS idx_vouchers_deleted_at ON vouchers (deleted_at);
CREATE TABLE IF NOT EXISTS wallets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL UNIQUE,
    available_balance bigint NOT NULL DEFAULT 0, pending_balance bigint NOT NULL DEFAULT 0, updated_at timestamp NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_wallets_tenant_id ON wallets (tenant_id);
CREATE TABLE IF NOT EXISTS bank_accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, bank_code varchar(255) NOT NULL,
    account_number varchar(255) NOT NULL, account_name varchar(255) NOT NULL, is_primary boolean NOT NULL DEFAULT true,
    created_at timestamp NOT NULL DEFAULT now(), deleted_at timestamp
);
CREATE INDEX IF NOT EXISTS idx_bank_accounts_tenant_id ON bank_accounts (tenant_id);
CREATE INDEX IF NOT EXISTS idx_bank_accounts_deleted_at ON bank_accounts (deleted_at);
CREATE TABLE IF NOT EXISTS subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(), enrollment_id uuid NOT NULL UNIQUE, tenant_id uuid NOT NULL,
    parent_id uuid NOT NULL, student_id uuid NOT NULL, billing_cycle varchar(50) NOT NULL, next_billing_date date NOT NULL,
    status varchar(50) NOT NULL DEFAULT 'active', billing_email varchar(255) NOT NULL, parent_name varchar(255) NOT NULL,
    class_name varchar(255) NOT NULL, amount bigint NOT NULL DEFAULT 0,
    created_at timestamp NOT NULL DEFAULT now(), updated_at timestamp NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_subscriptions_status_date ON subscriptions (status, next_billing_date);
CREATE INDEX IF NOT EXISTS idx_subscriptions_tenant_id ON subscriptions (tenant_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_parent_id ON subscriptions (parent_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_student_id ON subscriptions (student_id);
CREATE TABLE IF NOT EXISTS transactions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(), merchant_order_id varchar(255) NOT NULL UNIQUE,
    tenant_id uuid NOT NULL, parent_id uuid NOT NULL, student_id uuid NOT NULL, enrollment_id uuid NOT NULL,
    voucher_id uuid, subtotal_amount bigint NOT NULL, discount_amount bigint NOT NULL DEFAULT 0, gross_amount bigint NOT NULL,
    platform_fee bigint NOT NULL, payment_gateway_fee bigint NOT NULL DEFAULT 0, net_amount bigint NOT NULL,
    subscription_id uuid, billing_period_start date, currency varchar(50) NOT NULL DEFAULT 'IDR', status varchar(255) NOT NULL,
    is_sandbox boolean NOT NULL DEFAULT false, payment_gateway_provider varchar(255) DEFAULT 'duitku',
    payment_method varchar(255), payment_intent_id varchar(255) UNIQUE, checkout_session_url text, billing_email varchar(255) NOT NULL,
    payment_link_sent_at timestamp, last_reminder_sent_at timestamp, reminder_count integer NOT NULL DEFAULT 0,
    paid_at timestamp, created_at timestamp NOT NULL DEFAULT now(), updated_at timestamp NOT NULL DEFAULT now(), deleted_at timestamp,
    FOREIGN KEY (voucher_id) REFERENCES vouchers(id), FOREIGN KEY (subscription_id) REFERENCES subscriptions(id)
);
CREATE INDEX IF NOT EXISTS idx_transactions_tenant_status ON transactions (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_transactions_parent_id ON transactions (parent_id);
CREATE INDEX IF NOT EXISTS idx_transactions_student_id ON transactions (student_id);
CREATE INDEX IF NOT EXISTS idx_transactions_enrollment_id ON transactions (enrollment_id);
CREATE INDEX IF NOT EXISTS idx_transactions_subscription_id ON transactions (subscription_id);
CREATE INDEX IF NOT EXISTS idx_transactions_payment_intent_id ON transactions (payment_intent_id);
CREATE INDEX IF NOT EXISTS idx_transactions_deleted_at ON transactions (deleted_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_transactions_subscription_period ON transactions (subscription_id, billing_period_start) WHERE subscription_id IS NOT NULL AND billing_period_start IS NOT NULL;
CREATE TABLE IF NOT EXISTS ledger_entries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(), wallet_id uuid NOT NULL, reference_id uuid NOT NULL,
    reference_type varchar(255) NOT NULL, amount bigint NOT NULL, entry_type varchar(255) NOT NULL, description text,
    created_at timestamp NOT NULL DEFAULT now(), FOREIGN KEY (wallet_id) REFERENCES wallets(id)
);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_wallet_id ON ledger_entries (wallet_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ledger_payment_event ON ledger_entries (reference_id, reference_type, entry_type);
CREATE TABLE IF NOT EXISTS withdrawals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, bank_account_id uuid NOT NULL,
    amount bigint NOT NULL, admin_fee bigint NOT NULL DEFAULT 0, net_amount bigint NOT NULL, status varchar(255) NOT NULL,
    provider_payout_id varchar(255) UNIQUE, requested_at timestamp NOT NULL DEFAULT now(), processed_at timestamp,
    FOREIGN KEY (bank_account_id) REFERENCES bank_accounts(id)
);
CREATE INDEX IF NOT EXISTS idx_withdrawals_tenant_id ON withdrawals (tenant_id);
CREATE INDEX IF NOT EXISTS idx_withdrawals_bank_account_id ON withdrawals (bank_account_id);
CREATE INDEX IF NOT EXISTS idx_withdrawals_provider_payout_id ON withdrawals (provider_payout_id);
CREATE TABLE IF NOT EXISTS seed_versions (filename varchar(255) PRIMARY KEY, checksum varchar(64) NOT NULL, applied_at timestamp NOT NULL DEFAULT now());
