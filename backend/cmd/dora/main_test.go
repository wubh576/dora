package main

import "testing"

func TestValidateLoopbackAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{name: "loopback", addr: "127.0.0.1:8080"},
		{name: "all interfaces", addr: "0.0.0.0:8080", wantErr: true},
		{name: "invalid", addr: "127.0.0.1", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLoopbackAddress(test.addr)
			if test.wantErr && err == nil {
				t.Fatal("validateLoopbackAddress() 未返回错误")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateLoopbackAddress() 返回错误: %v", err)
			}
		})
	}
}
