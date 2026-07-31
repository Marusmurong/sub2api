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

// USDTPaymentIntent is the USDT-denominated side of a payment order.
//
// The order itself stays priced in CNY (amount / pay_amount), which is what
// keeps revenue reporting, daily limits and the cashback plugin working with no
// USDT-specific handling. Everything chain-specific — the receiving address,
// the exact figure the customer must transfer, and the exchange rate that was
// quoted — lives here.
//
// 删除策略：硬删除。意图随订单生命周期终结，状态字段已表达全部语义。
type USDTPaymentIntent struct {
	ent.Schema
}

func (USDTPaymentIntent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "usdt_payment_intents"},
	}
}

func (USDTPaymentIntent) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("order_id"),
		field.String("out_trade_no").
			MaxLen(64),
		field.String("provider_instance_id").
			MaxLen(64),

		// 链上参数
		field.String("address").
			MaxLen(64),
		field.String("network").
			MaxLen(16).
			Default("TRC20"),
		field.String("token_contract").
			MaxLen(64),

		// amount_usdt 存字符串而非浮点，这是刻意的：核销靠金额**精确相等**，
		// float64 表示不了 6 位小数的精确值，一次舍入就会让本该匹配的入金
		// 匹配不上（或更糟，让不该匹配的匹配上）。规范形式是 StringFixed(6)，
		// 与链上原生精度一致。
		field.String("amount_usdt").
			MaxLen(32),

		// 汇率快照：下单瞬间定价，订单有效期内不再变动，用户看到多少就付多少。
		// 同样以字符串保存，避免展示与计费出现浮点误差。
		field.String("rate").
			MaxLen(32),
		field.String("base_rate").
			MaxLen(32),
		field.String("premium_percent").
			MaxLen(16),
		field.String("rate_source").
			MaxLen(32),
		field.Time("rate_quoted_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		field.String("status").
			MaxLen(20).
			Default("PENDING"),
		field.String("matched_tx_hash").
			Optional().
			Nillable().
			MaxLen(80),

		// expires_at 比订单本身多留一段宽限，覆盖「用户卡点转账 + 链上确认延迟」。
		// 在此之前意图保持 PENDING，即使订单已被取消 —— 迟到的入金仍能自动核销，
		// 因为 toPaid 接受 CANCELLED 状态的订单。
		field.Time("expires_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

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

func (USDTPaymentIntent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id").Unique(),
		// 唯一尾数的强约束就在这里。应用层随机挑 00–99 再插入，冲突交给数据库
		// 判定 —— 应用层查重在并发下必然有 TOCTOU 窗口，而两张待支付订单撞上
		// 同一金额会让核销无法判断该给谁记账。
		index.Fields("address", "amount_usdt").
			Unique().
			Annotations(entsql.IndexWhere("status = 'PENDING'")),
		index.Fields("status", "expires_at"),
		index.Fields("out_trade_no"),
	}
}
