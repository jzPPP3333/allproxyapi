package antigravity

import (
	"context"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// Checkin reports that Antigravity has no daily check-in concept. The Cloud
// Code PA allowance is part of the Google plan and is not topped up by a
// client-side action, so there is nothing to call. Reporting this explicitly
// keeps a batch check-in run honest instead of silently skipping the provider.
func (c *Client) Checkin(_ context.Context, acc *pool.Account) (pool.CheckinResult, error) {
	result := pool.CheckinResult{
		At: time.Now(),
	}
	if acc != nil {
		result.AccountID = acc.ID
		result.AccountName = acc.Name
	}
	result.Status = pool.CheckinUnsupported
	result.Message = "Antigravity 的额度随 Google 套餐发放，无每日签到接口"
	return result, nil
}
