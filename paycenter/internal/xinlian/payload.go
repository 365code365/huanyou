package xinlian

import (
	"encoding/json"
	"strings"
)

// Action is what the cashier should do with a submit reply.
type Action struct {
	Kind   string            // redirect | qrcode | html | form | none
	URL    string            // redirect target, or a link shown next to the QR code
	QR     string            // QR code content
	HTML   string            // raw HTML (type=html)
	Fields map[string]string // form fields (Kind=form, posted to URL)
}

var (
	urlKeys  = []string{"pay_url", "payurl", "url", "jump_url", "h5_url", "mweb_url", "cashier_url", "deeplink", "scheme", "scheme_url"}
	qrKeys   = []string{"qrcode", "qr_code", "code_url", "qr", "qrcode_url", "qr_url"}
	htmlKeys = []string{"html", "form", "body"}
)

// ParsePayload maps the reply's type + payload (a string or an object whose
// field names depend on the channel) to an Action.
func ParsePayload(typ string, raw json.RawMessage) Action {
	typ = strings.ToLower(strings.TrimSpace(typ))
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s = strings.TrimSpace(s)
		switch {
		case s == "":
			return Action{Kind: "none"}
		case typ == "html" || strings.HasPrefix(s, "<"):
			return Action{Kind: "html", HTML: s}
		case typ == "qrcode" || typ == "scan":
			return Action{Kind: "qrcode", QR: s, URL: linkOf(s)}
		default:
			return Action{Kind: "redirect", URL: s}
		}
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return Action{Kind: "none"}
	}
	get := func(keys []string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	u, qr, h := get(urlKeys), get(qrKeys), get(htmlKeys)
	if action, _ := m["action"].(string); action != "" {
		if fields := stringMap(m["params"], m["fields"], m["data"]); fields != nil {
			return Action{Kind: "form", URL: action, Fields: fields}
		}
	}
	switch {
	case (typ == "qrcode" || typ == "scan") && (qr != "" || u != ""):
		if qr == "" {
			qr = u
		}
		return Action{Kind: "qrcode", QR: qr, URL: firstNonEmpty(u, linkOf(qr))}
	case h != "":
		return Action{Kind: "html", HTML: h}
	case u != "":
		return Action{Kind: "redirect", URL: u}
	case qr != "":
		return Action{Kind: "qrcode", QR: qr, URL: linkOf(qr)}
	}
	return Action{Kind: "none"}
}

func linkOf(s string) string {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func stringMap(vs ...any) map[string]string {
	for _, v := range vs {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		out := make(map[string]string, len(m))
		for k, x := range m {
			switch t := x.(type) {
			case string:
				out[k] = t
			case nil:
				out[k] = ""
			default:
				b, _ := json.Marshal(t)
				out[k] = string(b)
			}
		}
		return out
	}
	return nil
}
