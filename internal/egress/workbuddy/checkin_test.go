package workbuddy

import (
	"context"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

func TestCheckinUnsupportedWithoutPath(t *testing.T) {
	c := NewClient()
	acc := &pool.Account{ID: "wb-1", Name: "wb"}

	result, err := c.Checkin(context.Background(), acc, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != pool.CheckinUnsupported {
		t.Fatalf("status = %q, want unsupported", result.Status)
	}
	if result.AccountID != "wb-1" {
		t.Fatalf("result did not carry the account id: %+v", result)
	}
}

func TestClassifyCheckinWorkBuddy(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   pool.CheckinStatus
	}{
		{
			name:   "fresh check-in",
			status: 200,
			body:   `{"code":0,"msg":"success","data":{"reward":"10 积分","integral":10}}`,
			want:   pool.CheckinOK,
		},
		{
			name:   "already signed via flag",
			status: 200,
			body:   `{"code":0,"data":{"already":true}}`,
			want:   pool.CheckinAlready,
		},
		{
			name:   "already signed via english message",
			status: 200,
			body:   `{"code":0,"msg":"already checked in today"}`,
			want:   pool.CheckinAlready,
		},
		{
			name:   "already signed via chinese message",
			status: 200,
			body:   `{"code":0,"msg":"今日已签到"}`,
			want:   pool.CheckinAlready,
		},
		{
			name:   "quota exhausted body on 200",
			status: 200,
			body:   `{"code":6004,"msg":"insufficient credits"}`,
			want:   pool.CheckinFailed,
		},
		{
			name:   "server error",
			status: 500,
			body:   `{"msg":"boom"}`,
			want:   pool.CheckinFailed,
		},
		{
			name:   "not json",
			status: 502,
			body:   `<html>bad gateway</html>`,
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

func TestClassifyCheckinCollectsRewards(t *testing.T) {
	body := `{"code":0,"data":{"reward":"积分卡","rewards":["盲盒x1"],"integral":25}}`
	result := classifyCheckin(pool.CheckinResult{}, 200, []byte(body))

	want := []string{"积分卡", "盲盒x1", "25 积分"}
	if len(result.Rewards) != len(want) {
		t.Fatalf("rewards = %v, want %v", result.Rewards, want)
	}
	for i, reward := range want {
		if result.Rewards[i] != reward {
			t.Fatalf("rewards[%d] = %q, want %q", i, result.Rewards[i], reward)
		}
	}
}

func TestClassifyCheckinDoesNotDuplicateReward(t *testing.T) {
	// A deployment that reports the same reward under both fields must not
	// double-count it.
	body := `{"code":0,"data":{"reward":"积分","rewards":["积分"]}}`
	result := classifyCheckin(pool.CheckinResult{}, 200, []byte(body))
	if len(result.Rewards) != 1 {
		t.Fatalf("reward was duplicated: %v", result.Rewards)
	}
}

func TestClassifyCheckinFailureCarriesDiagnostic(t *testing.T) {
	result := classifyCheckin(pool.CheckinResult{}, 403, []byte(`{"msg":"forbidden"}`))
	if result.Status != pool.CheckinFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Message == "" {
		t.Fatal("a failure must carry a diagnostic message")
	}
}

func TestClientCheckinWithRealmAccount(t *testing.T) {
	// A fully populated account must still report unsupported when no path is
	// configured, rather than attempting a request against a guessed URL.
	acc := &pool.Account{ID: "wb-1", Realm: "cn", UID: "u1", AccessToken: "tok"}
	c := NewClient()

	result, err := c.Checkin(context.Background(), acc, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != pool.CheckinUnsupported {
		t.Fatalf("status = %q, want unsupported with no configured path", result.Status)
	}
	if result.AccountName == "" && result.AccountID == "" {
		t.Fatal("result did not identify the account")
	}
}
