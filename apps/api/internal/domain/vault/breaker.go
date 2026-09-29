package vault

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrVaultPausedByBreaker = errors.New("vault is paused by withdrawal circuit breaker pending manual review")
)

type OutflowBreakerConfig struct {
	Enabled          bool
	ThresholdPercent decimal.Decimal // e.g., 20.0 for 20%
	Window           time.Duration   // e.g., 1 * time.Hour
}

func DefaultOutflowBreakerConfig() OutflowBreakerConfig {
	return OutflowBreakerConfig{
		Enabled:          true,
		ThresholdPercent: decimal.NewFromInt(25),
		Window:           1 * time.Hour,
	}
}
