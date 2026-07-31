-- USDT (TRC20) 支付意图：订单的链上侧。
--
-- 订单本身仍以 CNY 计价（payment_orders.amount / pay_amount），收入看板、
-- 每日充值上限、充返插件因此完全不需要感知 USDT。链上相关的一切 —— 收款地址、
-- 用户必须精确转账的金额、下单时锁定的汇率 —— 都落在这张表。
--
-- amount_usdt / rate 系列刻意用 VARCHAR 而非 numeric：核销靠金额**精确相等**，
-- 规范形式统一为 6 位小数字符串（与 USDT 链上原生精度一致）。用浮点会因一次
-- 舍入让本该匹配的入金匹配不上，或让不该匹配的匹配上。
CREATE TABLE IF NOT EXISTS usdt_payment_intents (
    id                   BIGSERIAL PRIMARY KEY,
    order_id             BIGINT       NOT NULL,
    out_trade_no         VARCHAR(64)  NOT NULL,
    provider_instance_id VARCHAR(64)  NOT NULL,
    address              VARCHAR(64)  NOT NULL,
    network              VARCHAR(16)  NOT NULL DEFAULT 'TRC20',
    token_contract       VARCHAR(64)  NOT NULL,
    amount_usdt          VARCHAR(32)  NOT NULL,
    rate                 VARCHAR(32)  NOT NULL,
    base_rate            VARCHAR(32)  NOT NULL,
    premium_percent      VARCHAR(16)  NOT NULL,
    rate_source          VARCHAR(32)  NOT NULL,
    rate_quoted_at       TIMESTAMPTZ  NOT NULL,
    status               VARCHAR(20)  NOT NULL DEFAULT 'PENDING',
    matched_tx_hash      VARCHAR(80),
    expires_at           TIMESTAMPTZ  NOT NULL,
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS usdtpaymentintent_order_id
    ON usdt_payment_intents(order_id);

-- 唯一尾数（XXX.XXYY 的 YY）的强约束就在这里。应用层随机挑 00–99 再插入，
-- 冲突交给数据库判定 —— 应用层查重在并发下必然有 TOCTOU 窗口，而同一地址上
-- 两张待支付订单期望同一金额会让核销无法判断该给谁记账。
CREATE UNIQUE INDEX IF NOT EXISTS usdtpaymentintent_address_amount_usdt
    ON usdt_payment_intents(address, amount_usdt)
    WHERE status = 'PENDING';

CREATE INDEX IF NOT EXISTS usdtpaymentintent_status_expires_at
    ON usdt_payment_intents(status, expires_at);

CREATE INDEX IF NOT EXISTS usdtpaymentintent_out_trade_no
    ON usdt_payment_intents(out_trade_no);
