package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// TraceKey is a context.Context Value key. Its associated value should be a
// *Trace struct.
type TraceKey struct{}

// Trace contains a set of hooks for tracing events within a connection. Any
// specific hook may be nil.
type Trace struct {
	// DNSStart is called with the hostname of a DNS lookup before it begins.
	DNSStart func(name string)

	// DNSDone is called after a DNS lookup completes (or fails).  The coalesced
	// parameter is whether singleflight de-duped the call. The addrs are of type
	// net.IPAddr but can't actually be for circular dependency reasons.
	DNSDone func(netIPs []any, coalesced bool, err error)

	// ConnectStart is called before a Dial, excluding Dials made during DNS
	// lookups. In the case of DualStack (Happy Eyeballs) dialing, this may be
	// called multiple times, from multiple goroutines.
	ConnectStart func(network, addr string)

	// ConnectDone is called after a Dial with the results, excluding Dials made
	// during DNS lookups. It may also be called multiple times, like
	// ConnectStart.
	ConnectDone func(network, addr string, err error)
}

// Dialer is an implementation of net.Dialer that keeps track of network events
// and provides hooks for tracing.
type Dialer struct {
	*net.Dialer
}

func New() *Dialer {
	dialer := &net.Dialer{}
	resolver := &net.Resolver{
		// Augment the default dialer with timing information to measure the DNS
		// lookup time.
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if trace, ok := ctx.Value(TraceKey{}).(*Trace); ok {
				if trace.DNSStart != nil {
					trace.DNSStart(address)
				}
			}

			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}

			if trace, ok := ctx.Value(TraceKey{}).(*Trace); ok {
				if trace.DNSDone != nil {
					trace.DNSDone([]any{conn.RemoteAddr().String()}, false, nil)
				}
			}

			return conn, nil
		},
	}

	// Create a custom Dialer with a specified resolver function
	dialer.Resolver = resolver
	return &Dialer{dialer}
}

func WithClientTrace(ctx context.Context, trace *Trace) context.Context {
	if trace == nil {
		panic("nil trace")
	}

	return context.WithValue(ctx, TraceKey{}, trace)
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
	Host      string         `json:"host"`
	Addresses []string       `json:"addresses,omitempty"`
	DNSLookup Timings        `json:"dns_lookup,omitempty"`
	Connect   Timings        `json:"connect"`
	FirstByte FormatDuration `json:"first_byte"`
	Total     FormatDuration `json:"total"`
}

func main() {
	var results TestResults
	var roundTripTime, lookupTime, connTime time.Time

	roundTripTime = time.Now()
	trace := &Trace{
		DNSStart: func(string) {
			lookupTime = time.Now()
		},
		DNSDone: func([]any, bool, error) {
			results.DNSLookup.Operation = FormatDuration(time.Since(lookupTime))
			results.DNSLookup.Total = FormatDuration(time.Since(roundTripTime))
			// results.Addresses = mapS(dnsInfo.Addrs, func(addr net.IPAddr) string { return addr.String() })
		},
		ConnectStart: func(network, addr string) {
			connTime = time.Now()
		},
		ConnectDone: func(network, addr string, err error) {
			results.Connect.Operation = FormatDuration(time.Since(connTime))
			results.Connect.Total = FormatDuration(time.Since(roundTripTime))
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ctx = WithClientTrace(ctx, trace)

	dialer := New()

	// Perform DNS lookup and establish TCP connection
	conn, err := dialer.DialContext(ctx, "tcp", "statuscake.com:80")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer conn.Close()

	if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
		panic(err)
	}
}
