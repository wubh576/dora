package codex

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func TestParseMacOSSystemProxy(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		want   string
	}{
		{
			name: "Clash Verge HTTPS system proxy",
			output: `<dictionary> {
  HTTPSEnable : 1
  HTTPSPort : 7897
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7898
  SOCKSProxy : 127.0.0.1
}`,
			want: "http://127.0.0.1:7897",
		},
		{
			name: "ClashX HTTPS system proxy",
			output: `<dictionary> {
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
}`,
			want: "http://127.0.0.1:7890",
		},
		{
			name: "Shadowrocket SOCKS system proxy",
			output: `<dictionary> {
  HTTPSEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1086
  SOCKSProxy : ::1
}`,
			want: "socks5://[::1]:1086",
		},
		{
			name: "disabled",
			output: `<dictionary> {
  HTTPSEnable : 0
  SOCKSEnable : 0
}`,
		},
		{
			name: "invalid port",
			output: `<dictionary> {
  HTTPSEnable : 1
  HTTPSPort : 70000
  HTTPSProxy : 127.0.0.1
}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxyURL := parseMacOSSystemProxy([]byte(test.output))
			if test.want == "" {
				if proxyURL != nil {
					t.Fatalf("proxy = %s，期望 nil", proxyURL)
				}
				return
			}
			if proxyURL == nil || proxyURL.String() != test.want {
				t.Fatalf("proxy = %v，期望 %s", proxyURL, test.want)
			}
		})
	}
}

func TestResolveQuotaProxyPriority(t *testing.T) {
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "chatgpt.com"}}
	environmentURL := mustProxyURL(t, "http://127.0.0.1:8080")
	systemURL := mustProxyURL(t, "http://127.0.0.1:7897")
	fixtureError := errors.New("fixture proxy error")

	for _, test := range []struct {
		name                  string
		environmentURL        *url.URL
		environmentError      error
		environmentConfigured bool
		systemURL             *url.URL
		want                  string
		wantError             error
	}{
		{
			name:           "environment proxy wins",
			environmentURL: environmentURL,
			systemURL:      systemURL,
			want:           environmentURL.String(),
		},
		{
			name:                  "NO_PROXY keeps direct connection",
			environmentConfigured: true,
			systemURL:             systemURL,
		},
		{
			name:             "environment error is returned",
			environmentError: fixtureError,
			systemURL:        systemURL,
			wantError:        fixtureError,
		},
		{
			name:      "system proxy fallback",
			systemURL: systemURL,
			want:      systemURL.String(),
		},
		{
			name: "direct connection without proxy",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxyURL, err := resolveQuotaProxy(
				request,
				func(*http.Request) (*url.URL, error) {
					return test.environmentURL, test.environmentError
				},
				func(string) bool {
					return test.environmentConfigured
				},
				func() *url.URL {
					return test.systemURL
				},
			)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v，期望 %v", err, test.wantError)
			}
			if test.want == "" {
				if proxyURL != nil {
					t.Fatalf("proxy = %s，期望 nil", proxyURL)
				}
				return
			}
			if proxyURL == nil || proxyURL.String() != test.want {
				t.Fatalf("proxy = %v，期望 %s", proxyURL, test.want)
			}
		})
	}
}

func TestQuotaClientConfiguresProxyResolver(t *testing.T) {
	client := NewQuotaClient(nil)
	httpClient, ok := client.doer.(*http.Client)
	if !ok {
		t.Fatalf("doer type = %T，期望 *http.Client", client.doer)
	}
	transport, ok := httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T，期望 *http.Transport", httpClient.Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("quota transport 未配置 proxy resolver")
	}
}

func mustProxyURL(t *testing.T, value string) *url.URL {
	t.Helper()
	proxyURL, err := url.Parse(value)
	if err != nil {
		t.Fatalf("解析测试 proxy 失败: %v", err)
	}
	return proxyURL
}
