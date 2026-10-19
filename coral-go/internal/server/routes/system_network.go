// The QR code and the server's network addresses (moved out of system.go, #5003; no behaviour change).

package routes

import (
	"net"
	"net/http"
	"strings"

	"github.com/cdknorow/coral/internal/config"
	qrcode "github.com/skip2/go-qrcode"
)

// QRCode generates a QR code PNG for the given URL.
// GET /api/system/qr?url=<encoded_url>
func (h *SystemHandler) QRCode(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}
	png, err := qrcode.Encode(url, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	// The URL it encodes can carry a sign-in token: never kept by a browser or a shared cache (#3682).
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(png)
}

// NetworkInfo returns the server's LAN IP addresses and port.
// GET /api/system/network-info
func (h *SystemHandler) NetworkInfo(w http.ResponseWriter, r *http.Request) {
	var ips []string
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
				ips = append(ips, ipNet.IP.String())
			}
		}
	}
	primary := ""
	for _, ip := range ips {
		if strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "172.") {
			primary = ip
			break
		}
	}
	if primary == "" && len(ips) > 0 {
		primary = ips[0]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ips":                     ips,
		"primary":                 primary,
		"port":                    h.cfg.Port,
		"remote_access_effective": !config.IsLoopbackHost(h.cfg.Host),
		// The scheme other devices must use: with TLS on, plain HTTP is refused from the
		// network (#3355).
		"scheme": map[bool]string{true: "https", false: "http"}[h.cfg.TLS],
	})
}
