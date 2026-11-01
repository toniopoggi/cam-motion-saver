package engine

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Android supplies only an IPv4 address obtained from a non-VPN Wi-Fi network.
// Never enumerate all interfaces or fall back to a wildcard listener.
var viewerNetworkIP, viewerNetworkHost, viewerNetworkStatus string

// SetViewerNetwork updates the Wi-Fi listener independently of Nest recording.
// Empty ip immediately revokes LAN access, including existing connections.
// It is safe to call before Start; startup uses the latest supplied network.
func SetViewerNetwork(ip, hostname, status string) string {
	if ip != "" {
		address := net.ParseIP(ip)
		if address == nil || address.To4() == nil || !address.IsPrivate() {
			return "Viewer requires a private Wi-Fi IPv4 address"
		}
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	if hostname != "" && !validViewerHostname(hostname) {
		return "Invalid local viewer hostname"
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	viewerNetworkIP, viewerNetworkHost, viewerNetworkStatus = ip, hostname, status
	if state != nil {
		return state.setViewerNetwork(ip, hostname, status)
	}
	return ""
}

func validViewerHostname(host string) bool {
	label := strings.TrimSuffix(host, ".local")
	if label == host || len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, c := range label {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func (r *Runner) setViewerNetwork(ip, hostname, status string) string {
	r.networkMu.Lock()
	defer r.networkMu.Unlock()
	r.discoveryStatus = status
	if r.wifiServer != nil && r.wifiIP == ip {
		r.viewerHostname = hostname
		return ""
	}
	if r.wifiServer != nil {
		// Shutdown would allow old HTTP streams to survive a Wi-Fi change.
		_ = r.wifiServer.Close()
		r.wifiServer = nil
	}
	r.wifiIP, r.viewerHostname = "", ""
	if ip == "" {
		return ""
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(ip, "8767"))
	if err != nil {
		r.discoveryStatus = "Wi-Fi viewer unavailable: " + err.Error()
		return r.discoveryStatus
	}
	srv := &http.Server{Handler: r.server.Handler, ReadHeaderTimeout: 5 * time.Second}
	r.wifiServer, r.wifiIP, r.viewerHostname = srv, ip, hostname
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			r.log("error", fmt.Sprint("Wi-Fi viewer stopped: ", err))
		}
	}()
	return ""
}
