package repository

import (
	"context"
	"log"

	"github.com/redis/go-redis/v9"
)

// execScriptPipeline 执行一个含 Lua 脚本（Script.Run）的 pipeline，并在 NOSCRIPT 时重载脚本后重试一次。
//
// Script.Run 在 pipeline 中只会排入 EVALSHA，拿到 NOSCRIPT 也不会像直连那样回退 EVAL。
// 构造时预加载的脚本会因 Redis 重启、主从切换或 SCRIPT FLUSH 而丢失，此后 pipeline 里的
// 脚本调用全部静默失败。queue 每次都会被重新调用，因此它必须在内部重建自己收集的命令。
func execScriptPipeline(ctx context.Context, rdb *redis.Client, scripts []*redis.Script, queue func(pipe redis.Pipeliner)) {
	pipe := rdb.Pipeline()
	queue(pipe)
	cmds, _ := pipe.Exec(ctx)
	if !hasNoScriptError(cmds) {
		return
	}

	for _, script := range scripts {
		if err := script.Load(ctx, rdb).Err(); err != nil {
			log.Printf("[RedisScriptPipeline] Failed to reload Lua script: %v", err)
		}
	}
	pipe = rdb.Pipeline()
	queue(pipe)
	_, _ = pipe.Exec(ctx)
}

func hasNoScriptError(cmds []redis.Cmder) bool {
	for _, cmd := range cmds {
		if err := cmd.Err(); err != nil && redis.HasErrorPrefix(err, "NOSCRIPT") {
			return true
		}
	}
	return false
}
