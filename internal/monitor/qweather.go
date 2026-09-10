package monitor

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cheedonghu/news2tg/internal/config"
)

// qWeather 负责 JWT 和和风 API；每次请求生成短期令牌，避免常驻进程令牌过期。
type qWeather struct {
	baseURL                       string
	developerID, projectID, keyID string
	key                           ed25519.PrivateKey
}

func newQWeather(c config.QWeather) (*qWeather, error) {
	if c.DeveloperID == "" || c.ProjectID == "" || c.KeyID == "" || c.PrivateKeyPath == "" {
		return nil, fmt.Errorf("[qweather] developer_id、project_id、key_id、private_key_path 必须配置")
	}
	// 凭据只发到和风域名；不允许路径、用户信息或 HTTP 降级。
	host := strings.TrimSpace(c.APIHost)
	u, err := url.Parse("https://" + host)
	if err != nil || u.Hostname() == "" || !strings.HasSuffix(u.Hostname(), ".qweatherapi.com") || u.Host != host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
		return nil, fmt.Errorf("[qweather] api_host 必须为控制台提供的 qweatherapi.com 域名，不带协议或路径")
	}
	data, err := os.ReadFile(c.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("读取和风私钥: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("和风私钥必须为 PKCS8 PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析和风私钥失败")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("和风私钥必须使用 Ed25519")
	}
	return &qWeather{baseURL: u.String(), developerID: c.DeveloperID, projectID: c.ProjectID, keyID: c.KeyID, key: key}, nil
}

func (q *qWeather) token(now time.Time) string {
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": q.keyID})
	payload, _ := json.Marshal(map[string]any{"iss": q.developerID, "sub": q.projectID, "iat": now.Unix() - 30, "exp": now.Unix() + 900})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(q.key, []byte(input)))
}

func (q *qWeather) get(ctx context.Context, client *http.Client, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+q.token(time.Now()))
	// 禁止自动跳转，避免再次把重定向网页当作天气，也避免跨域携带认证。
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := bounded.Do(req)
	if err != nil {
		return fmt.Errorf("和风请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 和风返回结构化错误，保留错误类型便于区分凭据、权限和额度故障。
		var problem struct {
			Error struct {
				Type  string `json:"type"`
				Title string `json:"title"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&problem)
		return fmt.Errorf("和风接口 HTTP %d (%s, %s)", resp.StatusCode, problem.Error.Title, problem.Error.Type)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out); err != nil {
		return fmt.Errorf("和风响应不是有效 JSON: %w", err)
	}
	return nil
}

// fetch 先用 LocationID 查询名称和坐标，兼容项目已有城市编码，再请求新版逐日预报。
func (q *qWeather) fetch(ctx context.Context, client *http.Client, code, date string) (cityWeather, error) {
	var geo struct {
		Code     string                                `json:"code"`
		Location []struct{ ID, Name, Lat, Lon string } `json:"location"`
	}
	if err := q.get(ctx, client, "/geo/v2/city/lookup?location="+url.QueryEscape(code)+"&lang=zh", &geo); err != nil {
		return cityWeather{}, err
	}
	if geo.Code != "200" || len(geo.Location) == 0 {
		return cityWeather{}, fmt.Errorf("和风城市查询失败，code=%s", geo.Code)
	}
	city := geo.Location[0]
	if city.ID != code || city.Name == "" {
		return cityWeather{}, fmt.Errorf("和风返回城市与配置 ID 不匹配")
	}
	lat, e1 := strconv.ParseFloat(city.Lat, 64)
	lon, e2 := strconv.ParseFloat(city.Lon, 64)
	if e1 != nil || e2 != nil || !(lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180) {
		return cityWeather{}, fmt.Errorf("和风返回无效坐标")
	}
	type temperature struct {
		Value *float64 `json:"value"`
		Unit  string   `json:"unit"`
	}
	type period struct {
		Condition struct {
			Text string `json:"text"`
		} `json:"condition"`
	}
	var forecast struct {
		Days []struct {
			ForecastStartTime string      `json:"forecastStartTime"`
			TemperatureMax    temperature `json:"temperatureMax"`
			TemperatureMin    temperature `json:"temperatureMin"`
			Daytime           period      `json:"daytime"`
			Nighttime         period      `json:"nighttime"`
		} `json:"days"`
	}
	path := fmt.Sprintf("/weather/v1/daily/%.2f/%.2f?days=3&localTime=true&lang=zh", lat, lon)
	if err := q.get(ctx, client, path, &forecast); err != nil {
		return cityWeather{}, err
	}
	loc := time.FixedZone("CST", 8*3600)
	for _, day := range forecast.Days {
		start, err := time.Parse(time.RFC3339, day.ForecastStartTime)
		if err != nil { // API 也可能省略秒。
			start, err = time.Parse("2006-01-02T15:04Z07:00", day.ForecastStartTime)
		}
		if err != nil || start.In(loc).Format("2006-01-02") != date {
			continue
		}
		if day.TemperatureMax.Value == nil || day.TemperatureMin.Value == nil || day.TemperatureMax.Unit != "°C" || day.TemperatureMin.Unit != "°C" || day.Daytime.Condition.Text == "" {
			return cityWeather{}, fmt.Errorf("和风当天预报字段缺失或温度单位不符")
		}
		text := day.Daytime.Condition.Text
		if night := day.Nighttime.Condition.Text; night != "" && night != text {
			text += "转" + night
		}
		return cityWeather{Name: city.Name, Weather: text, High: fmt.Sprintf("%g℃", *day.TemperatureMax.Value), Low: fmt.Sprintf("%g℃", *day.TemperatureMin.Value)}, nil
	}
	return cityWeather{}, fmt.Errorf("和风响应缺少 %s 的预报", date)
}
