package ipclass

import "testing"

func TestOf(t *testing.T) {
	for ip, want := range map[string]string{
		"114.114.114.114": CN, "223.5.5.5": CN, "36.112.0.1": CN,
		"8.8.8.8": Abroad, "64.83.22.32": Abroad, "1.1.1.1": Abroad,
		"192.168.1.5": Private, "10.1.2.3": Private, "127.0.0.1": Private, "100.64.1.1": Private,
		"2001:4860:4860::8888": Abroad, "240e::1": CN,
		"bad": Unknown,
	} {
		if got := Of(ip); got != want {
			t.Errorf("Of(%s) = %q, want %q", ip, got, want)
		}
	}
}
