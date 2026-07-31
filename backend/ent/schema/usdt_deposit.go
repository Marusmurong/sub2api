package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// USDTDeposit is the ledger of every confirmed USDT transfer that has landed on
// one of our receiving addresses.
//
// It is not a cache of the chain. Every inbound transfer gets a row whether or
// not it matches an order, which is what makes three things possible at once:
// a transfer can only ever settle one order (replay guard), operators can see
// money that arrived without a matching order (wrong amount, late payment,
// mistaken transfer), and the wallet balance can be reconciled against our own
// books.
//
// 删除策略：硬删除。台账只追加，清理按时间范围批量删除即可。
type USDTDeposit struct {
	ent.Schema
}

func (USDTDeposit) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "usdt_deposits"},
	}
}

func (USDTDeposit) Fields() []ent.Field {
	return []ent.Field{
		field.String("tx_hash").
			MaxLen(80),
		field.String("address").
			MaxLen(64),
		field.String("from_address").
			MaxLen(64),
		field.String("token_contract").
			MaxLen(64),

		// 与 usdt_payment_intents.amount_usdt 同样以 StringFixed(6) 规范字符串存储，
		// 两边必须用同一种规范形式，否则「精确相等」比较无从谈起。
		field.String("amount_usdt").
			MaxLen(32),

		field.Time("block_timestamp").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		// UNMATCHED（待处理）/ MATCHED（已核销）/ IGNORED（人工判定无需处理）
		field.String("status").
			MaxLen(20).
			Default("UNMATCHED"),
		field.Int64("matched_order_id").
			Optional().
			Nillable(),
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (USDTDeposit) Indexes() []ent.Index {
	return []ent.Index{
		// 防重放锚点：同一笔链上转账重复扫描到时插入失败，回读既有行即可。
		//
		// 键里没有 log_index —— TronGrid 的 trc20 流水接口不返回它。用
		// (tx_hash, address, amount) 代替意味着：同一笔交易中向同一地址转出两笔
		// **完全相同**金额时，只会记为一条。这个方向是刻意选的：宁可少记一笔
		// 转人工，也不能多记一笔凭空发余额。何况唯一尾数保证了同地址上不会有
		// 两张待支付订单期望同一金额，第二笔无论如何都要人工处理。
		index.Fields("tx_hash", "address", "amount_usdt").Unique(),
		index.Fields("address", "amount_usdt", "status"),
		index.Fields("status", "block_timestamp"),
		index.Fields("matched_order_id"),
	}
}
