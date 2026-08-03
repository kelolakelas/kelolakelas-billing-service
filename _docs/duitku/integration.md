# Duitku Integration

This service uses the Duitku v2 inquiry API:

- Inquiry endpoint: `${DUITKU_API_BASE_URL}/v2/inquiry`
- Request signature: HMAC-SHA256 of `merchantCode + merchantOrderId + paymentAmount`, keyed by `DUITKU_API_KEY`.
- `merchantOrderId` is the billing transaction UUID.
- Callback endpoint: `/api/v1/billing/webhooks/duitku`.
- Callback content type: `application/x-www-form-urlencoded`.
- Callback signature: HMAC-SHA256 of `merchantCode + amount + merchantOrderId`, keyed by `DUITKU_API_KEY`.
- A callback is successful only when `resultCode` is `00` and its amount equals the transaction `gross_amount`.

The official API documentation is available at https://docs.duitku.com/api/id.

Set `DUITKU_CALLBACK_URL` to the public callback URL in deployed environments. The local default is only suitable for development.
