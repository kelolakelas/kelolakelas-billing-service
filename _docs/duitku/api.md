# Duitku Payment Gateway API

Dokumentasi ini merangkum API Duitku Payment Gateway berdasarkan dokumentasi resmi:

- https://docs.duitku.com/api/id
- API version yang dirujuk: v2.0

Dokumen ini juga menjelaskan integrasi KelolaKelas Billing Service dan API Gateway.

## 1. Konfigurasi Environment

Billing service membaca konfigurasi melalui config loader, bukan `os.Getenv` di logic bisnis.

| Variable | Keterangan |
| --- | --- |
| `DUITKU_API_BASE_URL` | Base URL Duitku. Sandbox: `https://sandbox.duitku.com/webapi/api/merchant`; production: `https://passport.duitku.com/webapi/api/merchant` |
| `DUITKU_API_KEY` | API key project merchant. Jangan commit atau log nilainya. |
| `DUITKU_MERCHANT_CODE` | Kode merchant project Duitku. |
| `DUITKU_CALLBACK_URL` | URL publik callback. Sebaiknya diarahkan ke API Gateway: `https://<public-host>/api/v1/billing/webhooks/duitku`. |
| `DUITKU_RETURN_URL` | URL redirect setelah pelanggan menyelesaikan atau membatalkan pembayaran. |

API key dan merchant code harus berasal dari project/environment yang sama. Sandbox dan production memiliki host serta kredensial yang berbeda.

## 2. Endpoint Resmi

| Fitur | Sandbox | Production |
| --- | --- | --- |
| Get payment method | `POST https://sandbox.duitku.com/webapi/api/merchant/paymentmethod/getpaymentmethod` | `POST https://passport.duitku.com/webapi/api/merchant/paymentmethod/getpaymentmethod` |
| Create transaction/inquiry | `POST https://sandbox.duitku.com/webapi/api/merchant/v2/inquiry` | `POST https://passport.duitku.com/webapi/api/merchant/v2/inquiry` |
| Transaction status | `POST https://sandbox.duitku.com/webapi/api/merchant/transactionStatus` | `POST https://passport.duitku.com/webapi/api/merchant/transactionStatus` |

Semua request API ke Duitku menggunakan `Content-Type: application/json`, kecuali callback Duitku ke merchant yang menggunakan `application/x-www-form-urlencoded`.

## 3. Alur Integrasi KelolaKelas

1. Academic service mengirim `POST /internal/billing/transactions` setelah memverifikasi enrollment dan menggunakan internal service credential.
2. Billing service menerima data enrollment/nominal dari alur internal yang tervalidasi; tidak ada endpoint user-facing untuk membuat invoice.
3. Billing service membuat UUID transaction sebagai `merchantOrderId`.
4. `DuitkuAdapter` mengirim inquiry ke Duitku dan menyimpan `reference` serta `paymentUrl`.
5. Client diarahkan ke `checkout_session_url` atau `paymentUrl`.
6. Duitku mengirim callback ke `DUITKU_CALLBACK_URL`.
7. API Gateway meneruskan callback publik ke billing service tanpa JWT.
8. Billing service memvalidasi merchant code, nominal, merchant order ID, dan HMAC callback signature.
9. Callback `resultCode=00` menandai transaksi sebagai `paid`, memajukan `next_billing_date`, mengaktifkan enrollment, dan mengkredit `net_amount` ke wallet serta ledger tenant.

### Endpoint aplikasi KelolaKelas

| Method | URL | Auth |
| --- | --- | --- |
| `POST` | `/internal/billing/transactions` | Internal bearer credential dari academic service |
| `POST` | `/api/v1/billing/webhooks/duitku` | Publik, divalidasi dengan HMAC Duitku |

Callback tidak boleh dilindungi middleware JWT karena request berasal dari server Duitku.

## 4. Signature dan Keamanan

Dokumentasi resmi terbaru menggunakan HMAC-SHA256. MD5 dan metode signature lama ditandai obsolete oleh Duitku.

### 4.1 Signature inquiry

String yang ditandatangani:

```text
merchantCode + merchantOrderId + paymentAmount
```

Signature:

```text
HMAC_SHA256(stringToSign, apiKey)
```

Contoh Go:

```go
func signInquiry(merchantCode, merchantOrderID string, amount int64, apiKey string) string {
    message := merchantCode + merchantOrderID + strconv.FormatInt(amount, 10)
    mac := hmac.New(sha256.New, []byte(apiKey))
    _, _ = mac.Write([]byte(message))
    return hex.EncodeToString(mac.Sum(nil))
}
```

### 4.2 Signature callback

String yang ditandatangani:

```text
merchantCode + amount + merchantOrderId
```

`amount` harus digunakan sebagai string persis seperti yang diterima pada callback sebelum proses signature. Signature dibandingkan dengan `hmac.Equal`.

Callback harus ditolak jika salah satu kondisi berikut terpenuhi:

- `merchantCode` tidak sama dengan konfigurasi merchant.
- `amount`, `merchantOrderId`, atau `signature` kosong.
- HMAC tidak cocok.
- Nominal callback tidak sama dengan `Transactions.gross_amount`.
- `merchantOrderId` bukan UUID transaction yang dikenal.

Jangan mengandalkan IP address saja sebagai autentikasi. Duitku mendokumentasikan IP outgoing untuk kebutuhan allowlist tambahan:

- Production: `182.23.85.8`, `182.23.85.9`, `182.23.85.10`, `182.23.85.13`, `182.23.85.14`, `103.177.101.184`, `103.177.101.185`, `103.177.101.186`, `103.177.101.189`, `103.177.101.190`
- Sandbox: `182.23.85.11`, `182.23.85.12`, `103.177.101.187`, `103.177.101.188`

## 5. Get Payment Method

Endpoint ini opsional. Gunanya mengambil channel pembayaran aktif dari project merchant sebelum inquiry.

### Request

```json
{
  "merchantcode": "DXXXX",
  "amount": 10000,
  "datetime": "2026-08-02 19:30:00",
  "signature": "<hmac-sha256>"
}
```

Signature request:

```text
merchantcode + amount + datetime
```

### Response sukses

```json
{
  "paymentFee": [
    {
      "paymentMethod": "VA",
      "paymentName": "MAYBANK VA",
      "paymentImage": "https://images.duitku.com/hotlink-ok/VA.PNG",
      "totalFee": "0"
    }
  ],
  "responseCode": "00",
  "responseMessage": "SUCCESS"
}
```

`paymentMethod` dari response diteruskan ke inquiry. Daftar channel dapat berubah sesuai konfigurasi project merchant.

## 6. Create Transaction / Inquiry

### Request

```http
POST /webapi/api/merchant/v2/inquiry
Content-Type: application/json
```

```json
{
  "merchantCode": "DXXXX",
  "paymentAmount": 40000,
  "paymentMethod": "VC",
  "merchantOrderId": "2d8f7a0c-7a0a-4aa8-8e54-000000000001",
  "productDetails": "Class Subscription Enrollment",
  "additionalParam": "",
  "merchantUserInfo": "parent@example.com",
  "customerVaName": "John Doe",
  "email": "parent@example.com",
  "phoneNumber": "08123456789",
  "itemDetails": [
    {
      "name": "Class Subscription",
      "price": 40000,
      "quantity": 1
    }
  ],
  "customerDetail": {
    "firstName": "John",
    "lastName": "Doe",
    "email": "parent@example.com",
    "phoneNumber": "08123456789",
    "billingAddress": {
      "firstName": "John",
      "lastName": "Doe",
      "address": "Jl. Contoh Raya",
      "city": "Jakarta",
      "postalCode": "11530",
      "phone": "08123456789",
      "countryCode": "ID"
    },
    "shippingAddress": {
      "firstName": "John",
      "lastName": "Doe",
      "address": "Jl. Contoh Raya",
      "city": "Jakarta",
      "postalCode": "11530",
      "phone": "08123456789",
      "countryCode": "ID"
    }
  },
  "callbackUrl": "https://example.com/api/v1/billing/webhooks/duitku",
  "returnUrl": "https://example.com/payment/return",
  "signature": "<hmac-sha256>",
  "expiryPeriod": 1440
}
```

### Field request

| Field | Tipe | Wajib | Keterangan |
| --- | --- | --- | --- |
| `merchantCode` | string | Ya | Kode merchant Duitku. |
| `paymentAmount` | integer | Ya | Nominal IDR tanpa desimal. Minimum mengikuti aturan Duitku. |
| `paymentMethod` | string | Ya | Kode channel, misalnya `VC`, `BC`, `VA`, `DA`, atau `SP`. |
| `merchantOrderId` | string | Ya | ID unik dari merchant, maksimal 50 karakter. Di KelolaKelas: UUID transaction. |
| `productDetails` | string | Ya | Deskripsi produk atau layanan. |
| `additionalParam` | string | Tidak | Parameter tambahan URL-encoded jika dipakai. |
| `merchantUserInfo` | string | Tidak | ID atau email user merchant. |
| `customerVaName` | string | Bergantung channel | Nama yang ditampilkan pada konfirmasi pembayaran. |
| `email` | string | Ya | Email pelanggan. |
| `phoneNumber` | string | Tidak | Nomor telepon pelanggan. |
| `itemDetails` | array | Tidak | Detail item; total harga harus sama dengan `paymentAmount`. |
| `customerDetail` | object | Tidak | Detail pelanggan dan alamat. Wajib untuk channel tertentu seperti kartu kredit. |
| `callbackUrl` | string | Ya | URL publik untuk callback server-to-server. |
| `returnUrl` | string | Ya | URL redirect browser pelanggan. Jangan gunakan sebagai sumber kebenaran status. |
| `signature` | string | Ya | HMAC-SHA256 request. |
| `expiryPeriod` | integer | Tidak | Masa berlaku dalam menit. Default dan batas mengikuti channel. |

### Implementasi KelolaKelas

Adapter saat ini mengirim field inti berikut:

- `merchantCode` dari `DUITKU_MERCHANT_CODE`.
- `paymentAmount` dari `gross_amount` sebagai `int64`.
- `merchantOrderId` dari UUID `Transactions.id`.
- `productDetails` dari judul subscription.
- `email`, `phoneNumber`, dan `customerVaName` dari detail user.
- `callbackUrl` dan `returnUrl` dari config loader.
- `paymentMethod` default `VC`.
- `expiryPeriod` `1440` menit.

### Response sukses

```json
{
  "merchantCode": "DXXXX",
  "reference": "DXXXXCX80TZJ85Q70QCI",
  "paymentUrl": "https://sandbox.duitku.com/topup/topupdirectv2.aspx?ref=example",
  "vaNumber": "7007014001444348",
  "qrString": "<qris-string>",
  "appUrl": "<optional-app-url>",
  "amount": "40000",
  "statusCode": "00",
  "statusMessage": "SUCCESS"
}
```

`reference` harus disimpan untuk rekonsiliasi dan pengecekan status. `paymentUrl` digunakan untuk checkout hosted Duitku.

## 7. Callback

Duitku mengirim callback setelah status pembayaran berubah.

```http
POST /api/v1/billing/webhooks/duitku
Content-Type: application/x-www-form-urlencoded
```

### Contoh callback

```text
merchantCode=DXXXX
&amount=40000
&merchantOrderId=2d8f7a0c-7a0a-4aa8-8e54-000000000001
&productDetail=Class+Subscription
&paymentCode=VC
&resultCode=00
&merchantUserId=parent%40example.com
&reference=DXXXXCX80TZJ85Q70QCI
&signature=<hmac-sha256>
&publisherOrderId=MGUHWKJX3M1KMSQN5
&settlementDate=2026-08-02
```

### Field callback

| Field | Keterangan |
| --- | --- |
| `merchantCode` | Kode project merchant. |
| `amount` | Nominal pembayaran sebagai string. |
| `merchantOrderId` | ID order merchant; di KelolaKelas sama dengan UUID transaction. |
| `productDetail` | Detail produk. |
| `paymentCode` | Channel pembayaran yang dipakai. |
| `resultCode` | `00` sukses, `01` gagal, `02` canceled/pending sesuai alur Duitku. |
| `merchantUserId` | ID atau email user merchant. |
| `reference` | Reference Duitku. |
| `signature` | HMAC callback. |
| `publisherOrderId` | ID pembayaran publisher Duitku. |
| `spUserHash` | Informasi tambahan untuk channel Shopee tertentu. |
| `settlementDate` | Estimasi tanggal settlement, format `YYYY-MM-DD`. |
| `issuerCode` | Kode issuer QRIS jika tersedia. |
| `customerName` | Identitas akun issuer untuk channel QRIS tertentu. |

Server callback mengembalikan HTTP `200 OK` setelah callback valid dan paid state beserta pekerjaan aktivasi durable berhasil disimpan. Kegagalan sementara pada academic tidak memaksa callback diulang karena worker billing akan melakukan retry; kegagalan penyimpanan callback tetap menghasilkan error agar provider dapat mengirim ulang callback.

### Aturan pemrosesan billing service

- `resultCode=00`: transaksi menjadi `paid` secara idempotent.
- `resultCode=01` atau `02`: transaksi menjadi `failed` sesuai mapping aplikasi.
- Callback transaksi yang sudah `paid` tidak boleh mengkredit wallet dua kali.
- Aktivasi enrollment yang gagal setelah commit pembayaran disimpan di `payment_reconciliations` dan dicoba ulang oleh worker billing secara idempotent.
- `amount` harus sama dengan `gross_amount` transaksi.
- Kredit ledger memakai `net_amount`, bukan `gross_amount`.
- Subscription `next_billing_date` dihitung ulang berdasarkan billing cycle.
- Redirect browser tidak digunakan untuk mengubah status transaksi.

## 8. Redirect

Redirect adalah alur browser, bukan callback server-to-server.

Contoh URL:

```text
GET https://example.com/payment/return?merchantOrderId=<order-id>&resultCode=00&reference=<reference>
```

Parameter:

| Field | Keterangan |
| --- | --- |
| `merchantOrderId` | ID order merchant. |
| `resultCode` | Informasi hasil yang ditampilkan ke user. |
| `reference` | Reference Duitku. |

`resultCode` pada redirect dapat dimanipulasi oleh user melalui URL. Gunakan callback tervalidasi atau transaction status API untuk keputusan finansial.

## 9. Transaction Status

Endpoint ini dapat digunakan untuk verifikasi atau rekonsiliasi transaksi.

```http
POST /webapi/api/merchant/transactionStatus
Content-Type: application/json
```

### Request

```json
{
  "merchantCode": "DXXXX",
  "merchantOrderId": "2d8f7a0c-7a0a-4aa8-8e54-000000000001",
  "signature": "<hmac-sha256>"
}
```

Signature:

```text
merchantCode + merchantOrderId
```

### Response

```json
{
  "merchantOrderId": "2d8f7a0c-7a0a-4aa8-8e54-000000000001",
  "reference": "DXXXXCX80TZJ85Q70QCI",
  "amount": "40000",
  "fee": "0.00",
  "statusCode": "00",
  "statusMessage": "SUCCESS"
}
```

Status yang didokumentasikan Duitku:

- `00`: sukses.
- `01`: pending atau gagal sesuai konteks response.
- `02`: canceled.

Jangan melakukan polling agresif atau cron request berulang tanpa backoff karena Duitku menerapkan batas hit rate.

## 10. Payment Method

| Kategori | Kode | Contoh |
| --- | --- | --- |
| Credit Card | `VC` | Visa, Mastercard, JCB |
| Virtual Account | `BC`, `M2`, `VA`, `I1`, `B1`, `BT`, `BR`, `DM`, `BV` | BCA, Mandiri, Maybank, BNI, CIMB, Permata, BRIVA, Danamon, BSI |
| Retail | `FT`, `IR` | Pegadaian/Alfa/Pos, Indomaret |
| E-Wallet | `OV`, `SA`, `LF`, `LA`, `DA` | OVO, Shopee Pay, LinkAja, DANA |
| Account Link | `SL`, `OL` | Shopee Pay, OVO |
| QRIS | `SP`, `NQ`, `GQ`, `SQ` | Shopee Pay, Nobu, Gudang Voucher, Nusapay |
| Paylater | `DN`, `AT` | Indodana, ATOME |
| E-Banking | `JP` | Jenius Pay |
| E-Commerce | `T1`, `T2`, `T3` | Tokopedia Card, Wallet, Others |

Channel yang tersedia bergantung pada aktivasi project merchant. Untuk channel kredit, `customerDetail` dan `itemDetails` wajib. Untuk channel e-commerce, `customerVaName` wajib.

## 11. Object JSON

### ItemDetails

```json
{
  "name": "Class Subscription",
  "price": 40000,
  "quantity": 1
}
```

`price` dan seluruh nominal harus integer tanpa desimal. Jumlah semua `price * quantity` harus sama persis dengan `paymentAmount`.

### CustomerDetail

```json
{
  "firstName": "John",
  "lastName": "Doe",
  "email": "parent@example.com",
  "phoneNumber": "08123456789",
  "billingAddress": {},
  "shippingAddress": {}
}
```

### Address

```json
{
  "firstName": "John",
  "lastName": "Doe",
  "address": "Jl. Contoh Raya",
  "city": "Jakarta",
  "postalCode": "11530",
  "phone": "08123456789",
  "countryCode": "ID"
}
```

### AccountLink

Account link membutuhkan `credentialCode` dan detail khusus channel OVO atau Shopee. Gunakan dokumentasi Account Linking Duitku untuk implementasi penuh.

```json
{
  "credentialCode": "<credential-code>",
  "ovo": {
    "paymentDetails": [
      {"paymentType": "CASH", "amount": 40000}
    ]
  },
  "shopee": {
    "useCoin": false,
    "promoId": ""
  }
}
```

### CreditCardDetail

```json
{
  "acquirer": "014",
  "binWhitelist": ["014", "022", "400000"]
}
```

## 12. Expiry Period

Nilai expiry adalah menit dan bergantung pada channel. Nilai default atau maksimum tidak selalu sama dengan nilai request.

| Channel | Default umum | Batas dokumentasi |
| --- | ---: | ---: |
| Credit Card | 30 | mengikuti aturan channel |
| Virtual Account | 1440 | lebih dari 1440 dapat diizinkan |
| Retail | 1440 | lebih dari 1440 dapat diizinkan |
| OVO | 10 | 1440 |
| Shopee Pay Apps | 10 | 60 |
| LinkAja | 24 | 1440 |
| DANA | 1440 | 1440 |
| QRIS | 10 | 60 |
| Nobu QRIS | 24 | 1440 |
| Indodana | 1440 | 1440 |
| ATOME | 720 | 720 |
| Jenius Pay | 10 | 10 |
| Tokopedia | 1440 | 1440 |

Adapter KelolaKelas menggunakan `1440` menit untuk inquiry.

## 13. HTTP Response dan Error

| HTTP | Kondisi umum |
| ---: | --- |
| `200` | Request berhasil. |
| `400` | Nominal di bawah minimum, melebihi maksimum, field wajib kosong, email invalid, atau panjang field melebihi batas. |
| `401` | Signature salah. |
| `404` | Merchant atau payment channel tidak ditemukan/aktif. |
| `409` | `paymentAmount` tidak sama dengan total item. |

Billing service meneruskan error Duitku sebagai error internal checkout dan mengembalikan response API aplikasi dengan format:

```json
{
  "status": "error",
  "message": "...",
  "data": null
}
```

## 14. Sandbox dan Testing

- Gunakan host sandbox dan kredensial sandbox untuk development.
- URL callback harus dapat diakses publik oleh Duitku; gunakan tunnel saat pengembangan lokal.
- Callback harus memakai HTTP/HTTPS port `80` atau `443` sesuai requirement Duitku.
- Verifikasi signature dengan API key sandbox yang benar.
- Uji idempotensi dengan mengirim callback sukses yang sama lebih dari sekali.
- Uji nominal salah, merchant code salah, signature salah, order ID tidak dikenal, dan result code gagal.
- Untuk kartu sandbox yang didokumentasikan Duitku: Visa `4000 0000 0000 0044`, expiry `03/33`, CVV `123`; Mastercard `5500 0000 0000 0004`, expiry `03/33`, CVV `123`.
- Gunakan demo transaksi sandbox Duitku untuk channel yang tidak memiliki kredensial khusus.

## 15. Changelog Penting API v2.0

- Juni 2026: callback menambahkan `customerName`.
- April 2026: HMAC menjadi metode signature utama; MD5 dan metode lama obsolete.
- Februari 2026: penambahan channel Tokopedia dan `appUrl`.
- Desember 2025: penghapusan parameter subscription lama dari inquiry.
- Agustus 2023: callback menambahkan `issuerCode` dan `settlementDate`.
- Januari 2023: callback menambahkan `publisherOrderId`.

Selalu cek dokumentasi resmi sebelum menambahkan channel atau field baru karena parameter dan aturan channel dapat berubah.
