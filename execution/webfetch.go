package execution

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	actool "github.com/Autumn-27/norma/tool"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Deliberately has no direct-connection retry. The sandbox firewall is a second
// boundary, not a substitute for honoring the broker on every fetch.
func newStrictWebFetch() actool.CoreTool {
	base := actool.NewWebFetch(actool.WebFetchConfig{})
	return actool.Build(actool.Spec{Name: base.Name(), Description: base.Description(), Schema: base.InputSchema(), Run: func(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
		var in struct {
			URL string `json:"url"`
		}
		if e := json.Unmarshal(input, &in); e != nil {
			return actool.Errorf(e.Error()), nil
		}
		target, e := url.Parse(in.URL)
		if e != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
			return actool.Errorf("HTTP(S) URL required"), nil
		}
		proxy, e := url.Parse(os.Getenv("HTTP_PROXY"))
		if e != nil || proxy.Host == "" {
			return actool.Errorf("Mandatory broker proxy is unavailable"), nil
		}
		raw, e := os.ReadFile(os.Getenv("CURL_CA_BUNDLE"))
		if e != nil {
			return actool.Errorf("Broker CA unavailable"), nil
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(raw) {
			return actool.Errorf("Invalid broker CA"), nil
		}
		transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
		req, e := http.NewRequestWithContext(ctx, "GET", in.URL, nil)
		if e != nil {
			return actool.Errorf(e.Error()), nil
		}
		res, e := client.Do(req)
		if e != nil {
			return actool.Errorf(e.Error()), nil
		}
		defer res.Body.Close()
		body, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
		if e != nil {
			return actool.Errorf("Incomplete body: " + e.Error()), nil
		}
		truncated := len(body) > 2<<20
		if truncated {
			body = body[:2<<20]
		}
		text := fmt.Sprintf("HTTP %d; response bytes retained=%d; truncated=%t; broker truncated=%q; broker incomplete=%q; layer=HTTP\n%s", res.StatusCode, len(body), truncated, res.Header.Get("X-Artex-Truncated"), res.Header.Get("X-Artex-Incomplete"), body)
		if res.Header.Get("X-Artex-Incomplete") != "" {
			return actool.Errorf("INCOMPLETE RESPONSE: retained bytes are only a partial observation.\n" + text), nil
		}
		return actool.Text(text), nil
	}})
}
