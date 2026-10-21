package main

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// endpoint is a validated loopback destination; hosts are numeric except localhost.
type endpoint struct {
	Host string
	Port string
}

// callback extracts explicit loopback routing while keeping the original authorization URL intact.
func callback(raw string) (*endpoint, error) {
	if len(raw) == 0 || len(raw) > 64*1024 || strings.ContainsAny(raw, "\x00\r\n") {
		return nil, fmt.Errorf("invalid browser URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("expected an HTTP or HTTPS browser URL")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid URL parameters")
	}
	redirect := u
	if values, ok := q["redirect_uri"]; ok {
		if len(values) != 1 {
			return nil, fmt.Errorf("ambiguous callback URL")
		}
		redirect, err = url.Parse(values[0])
		if err != nil || redirect.Host == "" || redirect.User != nil {
			return nil, fmt.Errorf("invalid callback URL")
		}
	}
	host := strings.ToLower(redirect.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, nil
	}
	if redirect.Scheme != "http" {
		return nil, fmt.Errorf("local HTTPS callbacks are not supported")
	}
	port := redirect.Port()
	if err := validEndpoint(host, port); err != nil {
		return nil, err
	}
	return &endpoint{Host: host, Port: port}, nil
}

// validEndpoint restricts callback channels to unprivileged numeric loopback endpoints.
func validEndpoint(host, port string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1024 || n > 65535 || strconv.Itoa(n) != port {
		return fmt.Errorf("callback needs an unprivileged explicit port")
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || strings.Contains(host, "%") {
		return fmt.Errorf("callback destination must be loopback")
	}
	return nil
}
