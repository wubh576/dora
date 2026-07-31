package codex

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const systemProxyLookupTimeout = time.Second

func quotaProxy(request *http.Request) (*url.URL, error) {
	return resolveQuotaProxy(
		request,
		http.ProxyFromEnvironment,
		proxyEnvironmentConfigured,
		macOSSystemProxy,
	)
}

func resolveQuotaProxy(
	request *http.Request,
	environmentProxy func(*http.Request) (*url.URL, error),
	environmentConfigured func(string) bool,
	systemProxy func() *url.URL,
) (*url.URL, error) {
	proxyURL, err := environmentProxy(request)
	if err != nil || proxyURL != nil || environmentConfigured(request.URL.Scheme) {
		return proxyURL, err
	}
	return systemProxy(), nil
}

func proxyEnvironmentConfigured(scheme string) bool {
	var names []string
	switch strings.ToLower(scheme) {
	case "http":
		names = []string{"HTTP_PROXY", "http_proxy"}
	case "https":
		names = []string{"HTTPS_PROXY", "https_proxy"}
	default:
		return false
	}
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func macOSSystemProxy() *url.URL {
	if runtime.GOOS != "darwin" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), systemProxyLookupTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return nil
	}
	return parseMacOSSystemProxy(output)
}

func parseMacOSSystemProxy(output []byte) *url.URL {
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(scanner.Text()), " : ")
		if found {
			values[key] = strings.TrimSpace(value)
		}
	}

	if proxyURL := fixedSystemProxy(values, "HTTPSEnable", "HTTPSProxy", "HTTPSPort", "http"); proxyURL != nil {
		return proxyURL
	}
	return fixedSystemProxy(values, "SOCKSEnable", "SOCKSProxy", "SOCKSPort", "socks5")
}

func fixedSystemProxy(
	values map[string]string,
	enableKey string,
	hostKey string,
	portKey string,
	scheme string,
) *url.URL {
	if values[enableKey] != "1" {
		return nil
	}
	host := strings.Trim(strings.TrimSpace(values[hostKey]), "[]")
	if host == "" || strings.ContainsAny(host, `/@?#`) {
		return nil
	}
	port, err := strconv.Atoi(values[portKey])
	if err != nil || port < 1 || port > 65535 {
		return nil
	}
	return &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}
}
