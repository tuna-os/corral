package main

import "testing"

func TestDisplayAddr(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{":8006", "localhost:8006"},
		{"0.0.0.0:8006", "0.0.0.0:8006"},
		{"127.0.0.1:8006", "127.0.0.1:8006"},
	}
	for _, tt := range tests {
		if got := displayAddr(tt.addr); got != tt.want {
			t.Errorf("displayAddr(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}
