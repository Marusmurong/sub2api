-- USDT 入金台账：每一笔打到我们收款地址的已确认转账。
--
-- 这不是链上数据的缓存。无论是否匹配上订单，每笔转入都会落一行，这同时解决
-- 三件事：一笔转账只能核销一张订单（防重放）、运营能看见「到账了但没匹配上」
-- 的钱（金额写错、迟到、误转）、钱包余额能与我们自己的账本对上。
CREATE TABLE IF NOT EXISTS usdt_deposits (
    id               BIGSERIAL PRIMARY KEY,
    tx_hash          VARCHAR(80)  NOT NULL,
    address          VARCHAR(64)  NOT NULL,
    from_address     VARCHAR(64)  NOT NULL,
    token_contract   VARCHAR(64)  NOT NULL,
    amount_usdt      VARCHAR(32)  NOT NULL,
    block_timestamp  TIMESTAMPTZ  NOT NULL,
    status           VARCHAR(20)  NOT NULL DEFAULT 'UNMATCHED',
    matched_order_id BIGINT,
    notes            TEXT,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- 防重放锚点：同一笔链上转账被重复扫描到时插入失败，回读既有行即可。
--
-- 键里没有 log_index —— TronGrid 的 trc20 流水接口不返回它。用
-- (tx_hash, address, amount) 代替意味着：同一笔交易向同一地址转出两笔**完全
-- 相同**金额时只会记为一条。这个方向是刻意选的：宁可少记一笔转人工，也不能
-- 多记一笔凭空发余额。
CREATE UNIQUE INDEX IF NOT EXISTS usdtdeposit_tx_hash_address_amount_usdt
    ON usdt_deposits(tx_hash, address, amount_usdt);

CREATE INDEX IF NOT EXISTS usdtdeposit_address_amount_usdt_status
    ON usdt_deposits(address, amount_usdt, status);

CREATE INDEX IF NOT EXISTS usdtdeposit_status_block_timestamp
    ON usdt_deposits(status, block_timestamp);

CREATE INDEX IF NOT EXISTS usdtdeposit_matched_order_id
    ON usdt_deposits(matched_order_id);
