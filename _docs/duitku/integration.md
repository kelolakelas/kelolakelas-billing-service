# Duitku Integration

This service uses the Duitku v2 inquiry API:

- Inquiry endpoint: `${DUITKU_API_BASE_URL}/v2/inquiry`
- Request signature: HMAC-SHA256 of `merchantCode + merchantOrderId + paymentAmount`, keyed by `DUITKU_API_KEY`.
- `merchantOrderId` is the billing transaction UUID.
- Callback endpoint: `/api/v1/billing/webhooks/duitku`.
- Callback content type: `application/x-www-form-urlencoded`.
- Callback signature: HMAC-SHA256 of `merchantCode + amount + merchantOrderId`, keyed by `DUITKU_API_KEY`.
- A callback is successful only when `resultCode` is `00` and its amount equals the transaction `gross_amount`.
- `expiryPeriod` is sent from `SUBSCRIPTION_PAYMENT_EXPIRY_PERIOD_DAYS` (default `14` days) and the same value is stored in `transactions.invoice_expires_at`, so the local expiry window matches the gateway window.
- Result codes other than `00`, `01`, and `02` leave the transaction unchanged; the service logs a structured warning and still answers `200` so Duitku does not retry.
- A `resultCode` of `00` is honoured even when the local expiry worker has already marked the transaction `expired`: the payment becomes `paid` and reconciliation runs as usual.
- Unpaid `pending` transactions past `invoice_expires_at` are moved to `expired` by the expiry worker (`TRANSACTION_EXPIRY_WORKER_ENABLED`, interval `TRANSACTION_EXPIRY_WORKER_INTERVAL_MINUTES`); the conditional update makes the job idempotent and safe across replicas. Expired transactions can be re-invoiced through the existing idempotent flow.

The official API documentation is available at https://docs.duitku.com/api/id.

Set `DUITKU_CALLBACK_URL` to the public callback URL in deployed environments. The local default is only suitable for development.
