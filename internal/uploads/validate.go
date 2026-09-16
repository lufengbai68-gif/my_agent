package uploads

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateReferenceURL 校验 reference_images[].url 是否在允许的域白名单内。
//
// SSRF 防御: 任何用户输入的 URL 不能引用任意公网, 必须是配置里列出的域(本系统上传到的对象存储)。
// 校验用 host 后缀匹配: 配置 "tos-cn-beijing.volces.com" 可匹配 "xxx.tos-cn-beijing.volces.com"。
func ValidateReferenceURL(rawURL string, allowedDomains []string) error {
	if rawURL == "" {
		return fmt.Errorf("empty url")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http(s), got %q", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if len(allowedDomains) == 0 {
		// 未配置白名单 -> 拒绝任何 URL (fail-closed)
		return fmt.Errorf("reference url rejected: no domains configured")
	}
	for _, d := range allowedDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return nil
		}
	}
	return fmt.Errorf("host %q not in allowed domains", host)
}
