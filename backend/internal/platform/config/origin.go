package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// canonicalOrigin matches browser serialization without accepting URL paths,
// credentials or query components as an origin. ASCII hostnames are intentional:
// operators must configure the browser's punycode form for international names.
func canonicalOrigin(raw string) (string, error) {
	invalid := errors.New("EVIA_PUBLIC_ORIGIN must be an ASCII http(s) origin without path, query, fragment or credentials")
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawPath != "" || strings.ContainsAny(raw, "?#") {
		return "", invalid
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", invalid
	}
	host := strings.ToLower(u.Hostname())
	for _, character := range host {
		if character <= 32 || character > 126 || character == '%' {
			return "", invalid
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		if strings.Contains(host, ":") {
			if four := ip.To4(); four != nil {
				// Browsers serialize IPv4-mapped IPv6 using hexadecimal hextets.
				host = fmt.Sprintf("::ffff:%x:%x", uint16(four[0])<<8|uint16(four[1]), uint16(four[2])<<8|uint16(four[3]))
			} else {
				host = ip.String()
			}
			host = "[" + host + "]"
		} else {
			host = ip.String()
		}
	} else {
		if strings.Contains(host, ":") || strings.HasPrefix(u.Host, "[") {
			return "", invalid
		}
		// Noncanonical numeric IPv4 forms are interpreted differently by browsers.
		last := host[strings.LastIndex(host, ".")+1:]
		numeric := last != ""
		for _, character := range last {
			numeric = numeric && character >= '0' && character <= '9'
		}
		if numeric || strings.HasPrefix(last, "0x") {
			return "", invalid
		}
	}
	port := u.Port()
	if port != "" {
		if !validPort(port) {
			return "", invalid
		}
		number, _ := strconv.Atoi(port)
		if !(scheme == "https" && number == 443 || scheme == "http" && number == 80) {
			host += ":" + strconv.Itoa(number)
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", invalid
	}
	return scheme + "://" + host, nil
}
