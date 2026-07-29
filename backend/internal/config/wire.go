package config

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminprobe"
	"github.com/google/wire"
)

// ProviderSet 提供配置层的依赖
var ProviderSet = wire.NewSet(
	ProvideConfig,
)

// ProvideConfig 提供应用配置
func ProvideConfig() (*Config, error) {
	cfg, err := LoadForBootstrap()
	if err != nil {
		return nil, err
	}
	// Multi-instance: share admin-probe token derived from JWT secret so self-looped
	// account/monitor checks accepted by any replica. Single-instance falls back to
	// process-local random when secret is empty at bootstrap.
	if cfg != nil && strings.TrimSpace(cfg.JWT.Secret) != "" {
		adminprobe.Configure(cfg.JWT.Secret)
	}
	return cfg, nil
}
