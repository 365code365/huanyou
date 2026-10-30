// Package geoip resolves an IP address to a country / city so the admin panel
// can auto-fill a node's region from just its address.
package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Location is the subset of geolocation we use.
type Location struct {
	IP          string `json:"ip"`
	CountryCode string `json:"country_code"` // ISO-3166 alpha-2, e.g. "US"
	Country     string `json:"country"`      // English name
	Region      string `json:"region"`       // state / province
	City        string `json:"city"`
}

// SuggestedName is a short, human-friendly node name, e.g. "美国 · 洛杉矶".
func (l Location) SuggestedName() string {
	cn := countryCN[l.CountryCode]
	if cn == "" {
		cn = l.Country
	}
	if cn == "" {
		return l.IP
	}
	if l.City != "" {
		return cn + " · " + l.City
	}
	return cn
}

// Lookup geolocates ip using the free ip-api.com service (no key required).
// It never blocks longer than a few seconds and returns a clear error on failure.
func Lookup(ctx context.Context, ip string) (*Location, error) {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		// Allow hostnames by resolving to the first IP.
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", ip)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("无法解析地址 %q", ip)
		}
		ip = ips[0].String()
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	url := "http://ip-api.com/json/" + ip + "?fields=status,message,country,countryCode,regionName,city,query"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("地理位置查询失败：%w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		RegionName  string `json:"regionName"`
		City        string `json:"city"`
		Query       string `json:"query"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("地理位置返回无法解析：%w", err)
	}
	if body.Status != "success" {
		msg := body.Message
		if msg == "" {
			msg = "未知错误"
		}
		return nil, fmt.Errorf("地理位置查询失败：%s", msg)
	}
	return &Location{
		IP: body.Query, CountryCode: strings.ToUpper(body.CountryCode),
		Country: body.Country, Region: body.RegionName, City: body.City,
	}, nil
}

// countryCN maps common country codes to Chinese names for nicer node names.
var countryCN = map[string]string{
	"US": "美国", "JP": "日本", "SG": "新加坡", "HK": "香港", "TW": "台湾",
	"KR": "韩国", "GB": "英国", "DE": "德国", "FR": "法国", "NL": "荷兰",
	"CA": "加拿大", "AU": "澳大利亚", "RU": "俄罗斯", "IN": "印度", "BR": "巴西",
	"TR": "土耳其", "VN": "越南", "TH": "泰国", "MY": "马来西亚", "ID": "印度尼西亚",
	"PH": "菲律宾", "AE": "阿联酋", "IT": "意大利", "ES": "西班牙", "SE": "瑞典",
	"CH": "瑞士", "FI": "芬兰", "NO": "挪威", "PL": "波兰", "UA": "乌克兰",
	"AR": "阿根廷", "MX": "墨西哥", "ZA": "南非", "CN": "中国",
}
