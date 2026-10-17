package wallet

import (
	"errors"
	"strings"
	"testing"
)

func TestOpenRejectsEmptyPassword(t *testing.T) {
	_, err := Open(Config{Host: "db", Name: "new-api", User: "pay_expire"})
	if err == nil {
		t.Fatal("空密码应拒绝")
	}
}

func TestRedactPassword(t *testing.T) {
	err := redact(errors.New("connect password=s3cret failed"), "s3cret")
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatal(err)
	}
}
