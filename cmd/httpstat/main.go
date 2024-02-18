package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type TLS struct {
	Version           uint16 `json:"version"`
	HandshakeComplete bool   `json:"handshake_complete"`
	CipherSuite       string `json:"cipher_suite"`
}

type Response struct {
	Status        string `json:"status"`
	Protocol      string `json:"protocol"`
	ContentLength int64  `json:"content_length"`
	ContentType   string `json:"content_type"`
	Body          []byte `json:"body"`
	TLS           TLS    `json:"tls"`
}

type FormatDuration time.Duration

func (d FormatDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

type Timings struct {
	Operation FormatDuration `json:"operation"`
	Total     FormatDuration `json:"total"`
}

type TestResults struct {
	Host         string         `json:"host"`
	Addresses    []string       `json:"addresses,omitempty"`
	DNSLookup    Timings        `json:"dns_lookup,omitempty"`
	Connect      Timings        `json:"connect"`
	TLSHandshake Timings        `json:"tls_handshake,omitempty"`
	FirstByte    FormatDuration `json:"first_byte"`
	Total        FormatDuration `json:"total"`

	Response *Response `json:"response,omitempty"`
	Error    string    `json:"error,omitempty"`
}

func mapS[T any, U any](s []T, f func(T) U) []U {
	ret := make([]U, len(s))
	for i, v := range s {
		ret[i] = f(v)
	}
	return ret
}

func main() {
	var results TestResults
	var roundTripTime, lookupTime, connTime, handshakeTime time.Time

	trace := &httptrace.ClientTrace{
		GetConn: func(string) {
			roundTripTime = time.Now()
		},
		DNSStart: func(dnsInfo httptrace.DNSStartInfo) {
			results.Host = dnsInfo.Host
			lookupTime = time.Now()
		},
		DNSDone: func(dnsInfo httptrace.DNSDoneInfo) {
			results.DNSLookup.Operation = FormatDuration(time.Since(lookupTime))
			results.DNSLookup.Total = FormatDuration(time.Since(roundTripTime))
			results.Addresses = mapS(dnsInfo.Addrs, func(addr net.IPAddr) string { return addr.String() })
		},
		ConnectStart: func(string, string) {
			connTime = time.Now()
		},
		ConnectDone: func(_, _ string, err error) {
			results.Connect.Operation = FormatDuration(time.Since(connTime))
			results.Connect.Total = FormatDuration(time.Since(roundTripTime))

			if err != nil {
				results.Error = err.Error()
			}
		},
		TLSHandshakeStart: func() {
			handshakeTime = time.Now()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			results.TLSHandshake.Operation = FormatDuration(time.Since(handshakeTime))
			results.TLSHandshake.Total = FormatDuration(time.Since(roundTripTime))

			if err != nil {
				results.Error = err.Error()
			}
		},
		GotConn: func(_ httptrace.GotConnInfo) {
			results.Total = FormatDuration(time.Since(roundTripTime))
		},
		GotFirstResponseByte: func() {
			results.FirstByte = FormatDuration(time.Since(roundTripTime))
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://statuscake.com/", nil)
	if err != nil {
		fmt.Println(err)
	}

	// Allows us to ignore TLS certificate errors.
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	followRedirects := true
	client := &http.Client{
		Transport: http.DefaultTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			if !followRedirects {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	res, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()

	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
	}

	results.Response = &Response{
		Status:        res.Status,
		Protocol:      res.Proto,
		ContentLength: res.ContentLength,
		ContentType:   res.Header.Get("Content-Type"),
		Body:          bodyBytes,
		TLS: TLS{
			Version:           res.TLS.Version,
			HandshakeComplete: res.TLS.HandshakeComplete,
			CipherSuite:       tls.CipherSuiteName(res.TLS.CipherSuite),
		},
	}

	if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
		panic(err)
	}
}
