package pkginfo

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// parsePlist decodes an XML property list into map[string]any / []any /
// string / []byte / int64 / bool / float64. Binary plists are not supported
// (Flutter's macOS build writes Info.plist as XML).
func parsePlist(b []byte) (any, error) {
	if bytes.HasPrefix(b, []byte("bplist")) {
		return nil, errors.New("不支持二进制 plist")
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	for {
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := t.(xml.StartElement); ok && se.Name.Local != "plist" {
			return plistValue(d, se)
		}
	}
}

func plistValue(d *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "dict":
		m := map[string]any{}
		var key string
		for {
			t, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := t.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					if key, err = plistText(d); err != nil {
						return nil, err
					}
					continue
				}
				v, err := plistValue(d, t)
				if err != nil {
					return nil, err
				}
				m[key] = v
			case xml.EndElement:
				return m, nil
			}
		}
	case "array":
		var a []any
		for {
			t, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := t.(type) {
			case xml.StartElement:
				v, err := plistValue(d, t)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			case xml.EndElement:
				return a, nil
			}
		}
	case "true", "false":
		if err := d.Skip(); err != nil {
			return nil, err
		}
		return se.Name.Local == "true", nil
	}
	s, err := plistText(d)
	if err != nil {
		return nil, err
	}
	switch se.Name.Local {
	case "data":
		return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	case "integer":
		return strconv.ParseInt(strings.TrimSpace(s), 0, 64)
	case "real":
		return strconv.ParseFloat(strings.TrimSpace(s), 64)
	}
	return s, nil // string, date
}

// plistText reads character data up to the element's end tag.
func plistText(d *xml.Decoder) (string, error) {
	var sb strings.Builder
	for {
		t, err := d.Token()
		if err == io.EOF {
			return "", io.ErrUnexpectedEOF
		} else if err != nil {
			return "", err
		}
		switch t := t.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.EndElement:
			return sb.String(), nil
		case xml.StartElement:
			if err := d.Skip(); err != nil {
				return "", err
			}
		}
	}
}

func plistString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}
