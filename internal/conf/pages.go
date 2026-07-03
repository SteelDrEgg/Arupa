package conf

import (
	"net/url"
	"strconv"
	"strings"
)

// GetPagePath returns the configured local page path for an HTTP status code.
func GetPagePath(statusCode int) (string, bool) {
	if statusCode < 100 || statusCode > 599 {
		return "", false
	}

	mu.RLock()
	defer mu.RUnlock()

	path := strings.TrimSpace(Conf.Pages[strconv.Itoa(statusCode)])
	if !isLocalPagePath(path) {
		return "", false
	}
	return path, true
}

func isLocalPagePath(path string) bool {
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return false
	}
	u, err := url.Parse(path)
	if err != nil {
		return false
	}
	return !u.IsAbs() && u.Host == "" && strings.HasPrefix(u.Path, "/")
}
