package web

import (
	"errors"
	"testing"

	"paycenter/internal/xinlian"
)

func TestMinAmount(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int64
	}{
		{&xinlian.APIError{Code: 4002, Msg: "订单金额低于最低限额 100 分"}, 100},
		{&xinlian.APIError{Code: 4002, Msg: "订单金额低于最低限额"}, 100},
		{&xinlian.APIError{Code: 4040, Msg: "当前支付通道信息不存在"}, 0},
		{errors.New("x"), 0},
		{nil, 0},
	} {
		if got := minAmount(c.err); got != c.want {
			t.Errorf("%v: got %d want %d", c.err, got, c.want)
		}
	}
}
