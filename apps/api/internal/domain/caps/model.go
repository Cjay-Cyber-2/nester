package caps

import (
	"context"
	"errors"
	"github.com/shopspring/decimal"
)


// ErrVaultCapExceeded is returned when crediting a deposit would push a
// vault's current_balance past its soft_capacity.
var ErrVaultCapExceeded = errors.New("deposit would exceed vault capacity limit")

// ErrUserDailyCapExceeded is returned when a deposit would push a user's
// trailing-24h deposit total past their daily_deposit_cap.
var ErrUserDailyCapExceeded = errors.New("deposit would exceed user daily deposit limit")

// CheckVaultCap reports whether depositing amount into a vault currently at
// currentBalance would exceed cap. A nil cap means no limit.
func CheckVaultCap(currentBalance decimal.Decimal, cap *decimal.Decimal, amount decimal.Decimal) error {
	if cap == nil {
		return nil
	}
	if currentBalance.Add(amount).GreaterThan(*cap) {
		return ErrVaultCapExceeded
	}
	return nil
}

// CheckUserDailyCap reports whether depositing amount, on top of a user's
// existing rolling24hTotal, would exceed cap. A nil cap means no limit.
func CheckUserDailyCap(rolling24hTotal decimal.Decimal, cap *decimal.Decimal, amount decimal.Decimal) error {
	if cap == nil {
		return nil
	}
	if rolling24hTotal.Add(amount).GreaterThan(*cap) {
		return ErrUserDailyCapExceeded
	}
	return nil
}
var (
	ErrTVLCapExceeded = errors.New("vault TVL cap exceeded on mainnet")
)

// VaultTVLCapManager defines the interface for checking and enforcing TVL caps per vault on mainnet.
type VaultTVLCapManager interface {
	CheckDepositCap(ctx context.Context, vaultID string, depositAmount float64) error
}
