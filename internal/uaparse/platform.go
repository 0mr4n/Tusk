package uaparse

import "github.com/Hulul-Group/Tusk/internal/domain"

// PlatformInfo is the platform-routing classification of a User-Agent
// (docs/UNIVERSAL-LINKS.md §4). It is independent of Kutt's Browser/OS buckets,
// which must stay byte-compatible with Kutt and are deliberately coarse.
type PlatformInfo struct {
	// Route is the rule key: ios|android|desktop, or "" when no rule applies
	// (unknown OS, Windows Phone, KaiOS, ...), which means the link target.
	Route domain.Platform
	// OS: ios|ipados|android|windows|macos|linux|chromeos|other.
	OS string
	// Device: mobile|tablet|desktop|other.
	Device string
	// InApp names an in-app webview (instagram, facebook, tiktok, ...) or "".
	InApp string
}

// Platform OS keys (stats buckets for tusk_route_visits.os).
const (
	OSiOS      = "ios"
	OSiPadOS   = "ipados"
	OSAndroid  = "android"
	OSWindows  = "windows"
	OSMacOS    = "macos"
	OSLinux    = "linux"
	OSChromeOS = "chromeos"
	OSOther    = "other"
)

// Device keys (stats buckets for tusk_route_visits.device).
const (
	DeviceMobile  = "mobile"
	DeviceTablet  = "tablet"
	DeviceDesktop = "desktop"
	DeviceOther   = "other"
)

// PlatformOSKeys and DeviceKeys list the stats buckets in display order.
var (
	PlatformOSKeys = []string{OSiOS, OSiPadOS, OSAndroid, OSWindows, OSMacOS, OSLinux, OSChromeOS, OSOther}
	DeviceKeys     = []string{DeviceMobile, DeviceTablet, DeviceDesktop, DeviceOther}
)

// Platform classifies ua. It uses no regexp and does not allocate for UAs up to
// 512 bytes.
//
// iPadOS 13+ Safari sends a desktop macOS UA and is therefore reported as
// macos/desktop. Only client-side code can tell it apart (the smart page does).
func Platform(ua string) PlatformInfo {
	var buf [lowerBuf]byte
	s := lowerInto(&buf, trim(ua))
	pi := PlatformInfo{InApp: inApp(s)}
	switch {
	case containsFold(s, "windows phone"), containsFold(s, "iemobile"):
		// Windows Phone UAs also say "Android" and "iPhone"; no app store to route to.
		pi.OS, pi.Device = OSOther, DeviceMobile
	case containsFold(s, "ipad"):
		pi.Route, pi.OS, pi.Device = domain.PlatformIOS, OSiPadOS, DeviceTablet
	case containsFold(s, "iphone"), containsFold(s, "ipod"):
		pi.Route, pi.OS, pi.Device = domain.PlatformIOS, OSiOS, DeviceMobile
	case containsFold(s, "android"):
		pi.Route, pi.OS = domain.PlatformAndroid, OSAndroid
		// Android phones send "Mobile"; tablets omit it (Google's UA guidance).
		if containsFold(s, "mobile") {
			pi.Device = DeviceMobile
		} else {
			pi.Device = DeviceTablet
		}
	case containsFold(s, "cros"):
		pi.Route, pi.OS, pi.Device = domain.PlatformDesktop, OSChromeOS, DeviceDesktop
	case containsFold(s, "windows nt"), containsFold(s, "win64"), containsFold(s, "win32"), containsFold(s, "windows;"):
		pi.Route, pi.OS, pi.Device = domain.PlatformDesktop, OSWindows, DeviceDesktop
	case containsFold(s, "macintosh"), containsFold(s, "mac os x"):
		pi.Route, pi.OS, pi.Device = domain.PlatformDesktop, OSMacOS, DeviceDesktop
	case containsFold(s, "x11"), containsFold(s, "linux"):
		pi.Route, pi.OS, pi.Device = domain.PlatformDesktop, OSLinux, DeviceDesktop
	default:
		pi.OS, pi.Device = OSOther, DeviceOther
		if containsFold(s, "mobile") {
			pi.Device = DeviceMobile
		}
	}
	return pi
}

// inApp detects common in-app browsers (webviews that often cannot open apps or
// stores, so the smart page offers "open in browser"). s is lower-cased.
func inApp(s string) string {
	switch {
	case containsFold(s, "instagram"):
		return "instagram"
	case containsFold(s, "fban/"), containsFold(s, "fbav/"), containsFold(s, "fb_iab"), containsFold(s, "fbios"):
		return "facebook"
	case containsFold(s, "musical_ly"), containsFold(s, "bytedancewebview"), containsFold(s, "tiktok"):
		return "tiktok"
	case containsFold(s, "snapchat"):
		return "snapchat"
	case containsFold(s, "linkedinapp"):
		return "linkedin"
	case containsFold(s, "twitter"):
		return "twitter"
	case containsFold(s, "pinterest"):
		return "pinterest"
	case containsFold(s, "micromessenger"):
		return "wechat"
	case containsFold(s, " line/"):
		return "line"
	}
	return ""
}
