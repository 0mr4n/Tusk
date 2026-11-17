package uaparse

import (
	"testing"

	"github.com/Hulul-Group/Tusk/internal/domain"
)

func TestPlatform(t *testing.T) {
	cases := []struct {
		name, ua     string
		route        domain.Platform
		os, dev, app string
	}{
		{"iphone safari", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1", domain.PlatformIOS, OSiOS, DeviceMobile, ""},
		{"ipod", "Mozilla/5.0 (iPod touch; CPU iPhone OS 15_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148", domain.PlatformIOS, OSiOS, DeviceMobile, ""},
		{"ipad legacy", "Mozilla/5.0 (iPad; CPU OS 12_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148", domain.PlatformIOS, OSiPadOS, DeviceTablet, ""},
		{"ipados13 desktop ua", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15", domain.PlatformDesktop, OSMacOS, DeviceDesktop, ""},
		{"iphone chrome", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/120.0 Mobile/15E148 Safari/604.1", domain.PlatformIOS, OSiOS, DeviceMobile, ""},
		{"android phone", "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36", domain.PlatformAndroid, OSAndroid, DeviceMobile, ""},
		{"android tablet", "Mozilla/5.0 (Linux; Android 13; SM-X700) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36", domain.PlatformAndroid, OSAndroid, DeviceTablet, ""},
		{"samsung browser", "Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/23.0 Chrome/115.0 Mobile Safari/537.36", domain.PlatformAndroid, OSAndroid, DeviceMobile, ""},
		{"windows chrome", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36", domain.PlatformDesktop, OSWindows, DeviceDesktop, ""},
		{"mac firefox", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14.1; rv:120.0) Gecko/20100101 Firefox/120.0", domain.PlatformDesktop, OSMacOS, DeviceDesktop, ""},
		{"linux", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36", domain.PlatformDesktop, OSLinux, DeviceDesktop, ""},
		{"ubuntu firefox", "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0", domain.PlatformDesktop, OSLinux, DeviceDesktop, ""},
		{"chromeos", "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36", domain.PlatformDesktop, OSChromeOS, DeviceDesktop, ""},
		{"windows phone", "Mozilla/5.0 (Windows Phone 10.0; Android 6.0.1; Microsoft; Lumia 950) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/52.0 Mobile Safari/537.36 Edge/15.15063", "", OSOther, DeviceMobile, ""},
		{"kaios", "Mozilla/5.0 (Mobile; Nokia_8110_4G; rv:48.0) Gecko/48.0 Firefox/48.0 KAIOS/2.5", "", OSOther, DeviceMobile, ""},
		{"curl", "curl/8.4.0", "", OSOther, DeviceOther, ""},
		{"empty", "", "", OSOther, DeviceOther, ""},
		{"instagram ios", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Instagram 307.0.0.34.111 (iPhone15,3; iOS 17_1; en_US)", domain.PlatformIOS, OSiOS, DeviceMobile, "instagram"},
		{"facebook android", "Mozilla/5.0 (Linux; Android 13; SM-G991B Build/TP1A; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/119.0 Mobile Safari/537.36 [FB_IAB/FB4A;FBAV/441.0.0.0;]", domain.PlatformAndroid, OSAndroid, DeviceMobile, "facebook"},
		{"tiktok", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 musical_ly_32.5.0 BytedanceWebview/d8a21c6", domain.PlatformIOS, OSiOS, DeviceMobile, "tiktok"},
		{"line", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Safari Line/13.20.0", domain.PlatformIOS, OSiOS, DeviceMobile, "line"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Platform(c.ua)
			want := PlatformInfo{Route: c.route, OS: c.os, Device: c.dev, InApp: c.app}
			if got != want {
				t.Fatalf("Platform(%q)\n got  %+v\n want %+v", c.ua, got, want)
			}
		})
	}
}

func TestPlatformZeroAlloc(t *testing.T) {
	ua := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Instagram 307.0"
	if n := testing.AllocsPerRun(200, func() { _ = Platform(ua) }); n != 0 {
		t.Fatalf("Platform allocs = %v", n)
	}
}

func BenchmarkPlatform(b *testing.B) {
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
	b.ReportAllocs()
	for b.Loop() {
		_ = Platform(ua)
	}
}
