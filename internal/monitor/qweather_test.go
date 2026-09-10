package monitor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"github.com/cheedonghu/news2tg/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQWeatherJWT(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	path := filepath.Join(t.TempDir(), "key.pem")
	os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600)
	q, err := newQWeather(config.QWeather{APIHost: "example.qweatherapi.com", DeveloperID: "dev", ProjectID: "project", KeyID: "kid", PrivateKeyPath: path})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	parts := strings.Split(q.token(now), ".")
	if len(parts) != 3 {
		t.Fatal("invalid JWT")
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("invalid signature")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	json.Unmarshal(raw, &claims)
	if claims["iss"] != "dev" || claims["sub"] != "project" || claims["iat"] != float64(now.Unix()-30) || claims["exp"].(float64) <= float64(now.Unix()) {
		t.Fatalf("invalid claims: %v", claims)
	}
	headerRaw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var header map[string]string
	json.Unmarshal(headerRaw, &header)
	if header["kid"] != "kid" || header["alg"] != "EdDSA" {
		t.Fatal("invalid JWT header")
	}
	for _, host := range []string{"", "https://example.qweatherapi.com", "example.qweatherapi.com/other", "example.org", "user@example.qweatherapi.com"} {
		_, err := newQWeather(config.QWeather{APIHost: host, DeveloperID: "dev", ProjectID: "project", KeyID: "kid", PrivateKeyPath: path})
		if err == nil {
			t.Fatalf("accepted invalid host %q", host)
		}
	}
}

func TestQWeatherRejectRedirect(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer srv.Close()
	q := &qWeather{baseURL: srv.URL, key: key}
	var out any
	if err := q.get(context.Background(), srv.Client(), "/", &out); err == nil {
		t.Fatal("redirect accepted")
	}
	if called {
		t.Fatal("redirect destination must not be called")
	}
}

func TestQWeatherDaily(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	for _, tc := range []struct {
		name, body string
		status     int
		bad        bool
	}{
		{"today", `{"days":[{"forecastStartTime":"2026-09-09T00:00+08:00","temperatureMax":{"value":99,"unit":"°C"}},{"forecastStartTime":"2026-09-10T00:00+08:00","temperatureMax":{"value":31,"unit":"°C"},"temperatureMin":{"value":23,"unit":"°C"},"daytime":{"condition":{"text":"多云"}},"nighttime":{"condition":{"text":"晴"}}}]}`, 200, false},
		{"missing today", `{"days":[]}`, 200, true},
		{"html", `<html>blocked</html>`, 200, true},
		{"unauthorized", `{}`, 401, true},
		{"missing temperature", `{"days":[{"forecastStartTime":"2026-09-10T00:00+08:00","daytime":{"condition":{"text":"晴"}}}]}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
					t.Error("missing JWT")
				}
				if r.URL.Path == "/geo/v2/city/lookup" {
					if r.URL.Query().Get("location") != "101010100" {
						t.Error("wrong location")
					}
					w.Write([]byte(`{"code":"200","location":[{"id":"101010100","name":"北京","lat":"39.904","lon":"116.408"}]}`))
					return
				}
				if r.URL.Path != "/weather/v1/daily/39.90/116.41" || r.URL.Query().Get("localTime") != "true" {
					t.Errorf("wrong daily URL %s", r.URL)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			q := &qWeather{baseURL: srv.URL, key: key}
			got, err := q.fetch(context.Background(), srv.Client(), "101010100", "2026-09-10")
			if (err != nil) != tc.bad {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if !tc.bad && (got.Name != "北京" || got.High != "31℃" || got.Low != "23℃" || got.Weather != "多云转晴") {
				t.Fatalf("bad result %+v", got)
			}
		})
	}
}
