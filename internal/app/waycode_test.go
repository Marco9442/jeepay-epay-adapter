package app_test

import (
	"testing"

	"github.com/marco9442/jeepay-epay-adapter/internal/app"
)

func TestWayCodeMapsAlipayToOrderCode(t *testing.T) {
	c := app.Config{WayCodeWxpay: "WX_NATIVE"}
	wx, err := c.WayCodeFor("wxpay")
	if err != nil || wx != "WX_NATIVE" {
		t.Fatalf("wxpay=%q err=%v", wx, err)
	}
	ali, err := c.WayCodeFor(" Alipay ")
	if err != nil || ali != "ALI_OC" {
		t.Fatalf("alipay=%q err=%v", ali, err)
	}
	if _, err := c.WayCodeFor("ali_pc"); err == nil {
		t.Fatal("ali_pc must be rejected")
	}
}
