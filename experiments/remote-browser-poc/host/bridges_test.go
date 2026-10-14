package main

import "testing"

// A page on the internet may not name a local address as its icon.
func TestLocalHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "app.localhost": true, "127.0.0.1": true, "::1": true,
		"10.0.0.8": true, "192.168.1.1": true, "169.254.169.254": true, "0.0.0.0": true,
		"example.com": false, "8.8.8.8": false, "": false, "localhost.example.com": false,
	} {
		if got := localHost(host); got != want {
			t.Errorf("localHost(%q) = %v, want %v", host, got, want)
		}
	}
}
