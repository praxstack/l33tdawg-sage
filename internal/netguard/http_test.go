package netguard

import "testing"

func TestLocalLANHTTPBase(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"loopback", "http://127.0.0.1:8080", true},
		{"localhost", "http://localhost:8080", true},
		{"private v4", "https://192.168.1.10:8443", true},
		{"ula v6", "https://[fd00::1]:8443", true},
		{"public metadata host", "http://169.254.169.254:80", false},
		{"public ip", "https://8.8.8.8:443", false},
		{"dns name", "https://example.com:443", false},
		{"userinfo", "http://u:p@127.0.0.1:8080", false},
		{"query", "http://127.0.0.1:8080?a=b", false},
		{"wrong scheme", "ftp://127.0.0.1:21", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LocalLANHTTPBase(tc.in, "http", "https")
			if (err == nil) != tc.ok {
				t.Fatalf("LocalLANHTTPBase(%q) err=%v, want ok=%v", tc.in, err, tc.ok)
			}
		})
	}
}

func TestLocalLANHostPort(t *testing.T) {
	if _, err := LocalLANHostPort("192.168.1.10:8080"); err != nil {
		t.Fatalf("private host:port rejected: %v", err)
	}
	if _, err := LocalLANHostPort("8.8.8.8:8080"); err == nil {
		t.Fatal("public host:port accepted")
	}
}
