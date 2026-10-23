// Package unifiapi reads clients and devices from a UniFi Network
// application through its official integration API (Network 9.0 and
// later), authenticated with an API key.
package unifiapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to one controller. BaseURL is the console address, such as
// https://192.168.1.1; the integration path is added automatically.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func New(baseURL, apiKey string, verifyTLS bool) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Consoles ship with a self-signed certificate, so verification is opt-in.
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: !verifyTLS, MinVersion: tls.VersionTLS12}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTP: &http.Client{Timeout: 30 * time.Second, Transport: tr}}
}

type Site struct {
	ID                string `json:"id"`
	InternalReference string `json:"internalReference"`
	Name              string `json:"name"`
}

type ClientInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"` // WIRED, WIRELESS, VPN, TELEPORT
	IPAddress  string `json:"ipAddress"`
	MACAddress string `json:"macAddress"`
}

type Device struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Model             string `json:"model"`
	MACAddress        string `json:"macAddress"`
	IPAddress         string `json:"ipAddress"`
	State             string `json:"state"`
	FirmwareVersion   string `json:"firmwareVersion"`
	FirmwareUpdatable bool   `json:"firmwareUpdatable"`
}

type page[T any] struct {
	Offset     int `json:"offset"`
	Limit      int `json:"limit"`
	Count      int `json:"count"`
	TotalCount int `json:"totalCount"`
	Data       []T `json:"data"`
}

// apiPrefix is where UniFi OS consoles expose the integration API. A
// self-hosted Network application serves it without the proxy prefix.
var apiPrefixes = []string{"/proxy/network/integration/v1/", "/integration/v1/"}

func (c *Client) get(ctx context.Context, prefix, path string, q url.Values, v any) error {
	u := c.BaseURL + prefix + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-KEY", c.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	case http.StatusNotFound:
		return errNotFound
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("controller answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
}

var (
	ErrAuth     = errors.New("the API key was rejected")
	errNotFound = errors.New("not found")
)

// list fetches every page of a collection.
func list[T any](ctx context.Context, c *Client, prefix, path string) ([]T, error) {
	var out []T
	for offset := 0; ; {
		var p page[T]
		q := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {"200"}}
		if err := c.get(ctx, prefix, path, q, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Data...)
		offset += len(p.Data)
		if len(p.Data) == 0 || offset >= p.TotalCount || len(out) > 100000 {
			return out, nil
		}
	}
}

// Snapshot is everything read from a controller in one sync.
type Snapshot struct {
	Sites   []Site
	Clients map[string][]ClientInfo // by site ID
	Devices map[string][]Device     // by site ID
}

// Fetch reads all sites with their clients and devices.
func (c *Client) Fetch(ctx context.Context) (*Snapshot, error) {
	var sites []Site
	var prefix string
	var err error
	for _, p := range apiPrefixes {
		sites, err = list[Site](ctx, c, p, "sites")
		if err == nil {
			prefix = p
			break
		}
		if !errors.Is(err, errNotFound) {
			return nil, err
		}
	}
	if err != nil {
		return nil, errors.New("no UniFi Network integration API found at this address (Network 9.0 or later is required)")
	}
	s := &Snapshot{Sites: sites, Clients: map[string][]ClientInfo{}, Devices: map[string][]Device{}}
	for _, site := range sites {
		cl, err := list[ClientInfo](ctx, c, prefix, "sites/"+url.PathEscape(site.ID)+"/clients")
		if err != nil {
			return nil, fmt.Errorf("clients of site %s: %w", site.Name, err)
		}
		dv, err := list[Device](ctx, c, prefix, "sites/"+url.PathEscape(site.ID)+"/devices")
		if err != nil {
			return nil, fmt.Errorf("devices of site %s: %w", site.Name, err)
		}
		// Older Network versions only report firmware on the detail endpoint.
		for i := range dv {
			if dv[i].FirmwareVersion != "" || i >= 1000 {
				continue
			}
			var d Device
			if err := c.get(ctx, prefix, "sites/"+url.PathEscape(site.ID)+"/devices/"+url.PathEscape(dv[i].ID), nil, &d); err == nil {
				dv[i].FirmwareVersion, dv[i].FirmwareUpdatable = d.FirmwareVersion, d.FirmwareUpdatable
			}
		}
		s.Clients[site.ID], s.Devices[site.ID] = cl, dv
	}
	return s, nil
}
