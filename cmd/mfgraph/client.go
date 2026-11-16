package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
)

// Connection settings: environment variables, overridden by flags.
const (
	envURL    = "MFGRAPH_URL"
	envToken  = "MFGRAPH_TOKEN"
	envPrefix = "MFGRAPH_API_PREFIX"

	defaultURL    = "http://127.0.0.1:8094"
	defaultPrefix = "/api/v1"
	// contractBase is the path prefix of the operations in the contract.
	contractBase = "/api/v1"
)

// conn is the connection to the server: a base URL (http(s)://host:port, or
// unix:<socket path>), a bearer token and the API path prefix.
type conn struct {
	url, token, prefix string
}

// errServer is returned after the server's error response has been
// printed as it was received.
var errServer = errors.New("the server answered with an error")

// prefixDoer sends the requests of the generated client, which uses the
// paths of the contract (/api/v1/...), below the configured API prefix.
type prefixDoer struct {
	client *http.Client
	prefix string
}

func (d prefixDoer) Do(req *http.Request) (*http.Response, error) {
	if d.prefix != contractBase {
		if rest, ok := strings.CutPrefix(req.URL.Path, contractBase); ok {
			req.URL.Path = d.prefix + rest
			req.URL.RawPath = ""
		}
	}
	return d.client.Do(req)
}

// client returns the generated HTTP client for c.
func (c conn) client() (*gen.Client, error) {
	hc := &http.Client{Timeout: 0}
	server := c.url
	if sock, ok := strings.CutPrefix(c.url, "unix:"); ok {
		if sock == "" {
			return nil, fmt.Errorf("%s: unix: needs a socket path", envURL)
		}
		hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", sock)
		}}
		server = "http://mfgraph"
	} else if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		return nil, fmt.Errorf("%s must be http://host:port, https://host:port or unix:<socket path>", envURL)
	}
	prefix := c.prefix
	if prefix == "" {
		prefix = defaultPrefix
	}
	return gen.NewClient(strings.TrimSuffix(server, "/"), gen.WithHTTPClient(prefixDoer{client: hc, prefix: prefix}),
		gen.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			if c.token != "" {
				req.Header.Set("Authorization", "Bearer "+c.token)
			}
			return nil
		}))
}

// reply is the result of a call of the generated client.
type reply struct {
	resp *http.Response
	err  error
}

// call wraps the two results of a client call, so that they can be passed
// on together: decode(call(c.GetProject(...)), ...).
func call(resp *http.Response, err error) reply { return reply{resp, err} }

// decode reads a successful JSON response into out; for any other status
// it prints the response body, as received, to stderr and returns
// errServer.
func decode(r reply, stderr io.Writer, out any, ok ...int) error {
	if r.err != nil {
		return r.err
	}
	resp := r.resp
	defer resp.Body.Close()
	if !statusIn(resp.StatusCode, ok) {
		return serverError(resp, stderr)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode the response: %w", err)
	}
	return nil
}

func statusIn(code int, ok []int) bool {
	if len(ok) == 0 {
		return code == http.StatusOK
	}
	for _, c := range ok {
		if c == code {
			return true
		}
	}
	return false
}

// serverError prints an error response as the server sent it.
func serverError(resp *http.Response, stderr io.Writer) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	fmt.Fprintf(stderr, "mfgraph: %s\n", resp.Status)
	if len(b) > 0 {
		stderr.Write(b)
		if b[len(b)-1] != '\n' {
			fmt.Fprintln(stderr)
		}
	}
	return errServer
}
