// Command paycenter-mock runs a simulated external payment center for trying
// the "下单链接" integration end to end without a real payment provider.
//
//	paycenter-mock -merchant HY1A2B3C4D -sign-key <hex> -enc-key <hex> [-listen 127.0.0.1:18200] [-drop-notify]
//
// Copy the merchant id and keys from 后台 → 系统设置 → 支付, then set there:
//
//	下单链接       http://127.0.0.1:18200/checkout
//	订单查询接口   http://127.0.0.1:18200/api/order/query
//
// (plain http is accepted only for 127.0.0.1 / localhost). Buying in the app
// opens the checkout page; "确认支付" marks the order paid and notifies the
// API. With -drop-notify the notification is never sent, so the order only
// completes through the API's active query. Never point a production system
// at this: anyone could "pay".
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"vpnplatform/server/internal/payment/paycenter"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18200", "listen address")
	mid := flag.String("merchant", "", "merchant id (from the admin panel)")
	sk := flag.String("sign-key", "", "sign key, hex")
	ek := flag.String("enc-key", "", "enc key, hex")
	drop := flag.Bool("drop-notify", false, "never send notifications (test the active query)")
	flag.Parse()
	c, err := paycenter.New(*mid, *sk, *ek)
	if err != nil {
		log.Fatal(err)
	}
	c.DropNotify = *drop
	fmt.Printf("模拟支付中心: http://%s\n  下单链接     http://%s/checkout\n  订单查询接口 http://%s/api/order/query\n  订单列表     http://%s/api/orders\n",
		*listen, *listen, *listen, *listen)
	log.Fatal(http.ListenAndServe(*listen, c.Handler()))
}
