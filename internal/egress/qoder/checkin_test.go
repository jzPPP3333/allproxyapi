package qoder

import (
	"context"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

func TestQoderCheckinUnsupportedWithoutPath(t *testing.T) {
	c := NewClient(configForTest())
	acc := &pool.Account{ID: "q-1", AccountName: "user@example.com"}

	result, err := c.Checkin(context.Background(), acc, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != pool.CheckinUnsupported {
		t.Fatalf("status = %q, want unsupported", result.Status)
	}
	if result.AccountID != "q-1" {
		t.Fatalf("result did not carry the account id: %+v", result)
	}
}

func TestClassifyCheckinQoder(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   pool.CheckinStatus
	}{
		{
			name:   "fresh check-in",
			status: 200,
			body:   `{"code":0,"message":"ok","data":{"reward":"50 credits","credits":50}}`,
			want:   pool.CheckinOK,
		},
		{
			name:   "already signed via flag",
			status: 200,
			body:   `{"code":0,"data":{"already":true}}`,
			want:   pool.CheckinAlready,
		},
		{
			name:   "already signed via message",
			status: 200,
			body:   `{"code":0,"message":"今天已签到"}`,
			want:   pool.CheckinAlready,
		},
		{
			name:   "quota exhausted",
			status: 200,
			body:   `{"code":6004,"message":"insufficient credits"}`,
			want:   pool.CheckinFailed,
		},
		{
			name:   "unauthorized",
			status: 401,
			body:   `{"code":401,"message":"unauthorized"}`,
			want:   pool.CheckinFailed,
		},
		{
			name:   "gateway error",
			status: 502,
			body:   `bad gateway`,
			want:   pool.CheckinFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := classifyCheckin(pool.CheckinResult{}, tc.status, []byte(tc.body))
			if result.Status != tc.want {
				t.Fatalf("status = %q, want %q (message: %q)", result.Status, tc.want, result.Message)
			}
		})
	}
}

func TestClassifyCheckinQoderRewards(t *testing.T) {
	body := `{"code":0,"data":{"reward":"每日礼包","rewards":["加速卡"],"credits":100}}`
	result := classifyCheckin(pool.CheckinResult{}, 200, []byte(body))

	want := []string{"每日礼包", "加速卡", "100 credits"}
	if len(result.Rewards) != len(want) {
		t.Fatalf("rewards = %v, want %v", result.Rewards, want)
	}
	for i, reward := range want {
		if result.Rewards[i] != reward {
			t.Fatalf("rewards[%d] = %q, want %q", i, result.Rewards[i], reward)
		}
	}
}

func TestClassifyCheckinQoderFailureDiagnostic(t *testing.T) {
	result := classifyCheckin(pool.CheckinResult{}, 500, []byte(`{"message":"internal error"}`))
	if result.Status != pool.CheckinFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Message != "internal error" {
		t.Fatalf("message = %q, want the upstream message", result.Message)
	}
}

func TestQoderCheckinDerivesFingerprint(t *testing.T) {
	c := NewClient(configForTest())
	acc := &pool.Account{ID: "q-2", AccountName: "user@example.com"}

	// The fingerprint must be established even when the check-in is skipped for
	// lack of a configured path, so a later call reuses the same identity.
	if _, err := c.Checkin(context.Background(), acc, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.MachineID == "" {
		t.Fatal("machine id was not derived during check-in")
	}
	if acc.MachineToken == "" {
		t.Fatal("machine token was not derived during check-in")
	}
}
