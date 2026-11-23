package carbon

import (
	"context"
	"encoding/json"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const electricityMapsEndpoint = "https://api.electricitymaps.com/v4/carbon-intensity/latest"

// wattTimeBaseURL is the WattTime v3 API base.
const wattTimeBaseURL = "https://api.watttime.org"

// Provider retrieves the latest grid carbon intensity.
// Source returns a stable identifier used as the carbon_source metric label
// (e.g. "electricitymaps" or "watttime").
type Provider interface {
	Fetch(ctx context.Context) (float64, error)
	// Source returns the provider name used as the carbon_source label.
	Source() string
}

// ---------------------------------------------------------------------------
// ElectricityMapsClient
// ---------------------------------------------------------------------------

// ElectricityMapsClient retrieves carbon intensity from Electricity Maps.
type ElectricityMapsClient struct {
	zone       string
	token      string
	httpClient *http.Client
}

// NewElectricityMapsClient creates an Electricity Maps API client.
func NewElectricityMapsClient(zone, token string, httpClient *http.Client) *ElectricityMapsClient {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 10 * time.Second,
		}
	}

	return &ElectricityMapsClient{
		zone:       strings.TrimSpace(zone),
		token:      strings.TrimSpace(token),
		httpClient: httpClient,
	}
}

// Source implements Provider.
func (c *ElectricityMapsClient) Source() string { return "electricitymaps" }

type electricityMapsResponse struct {
	CarbonIntensity *float64 `json:"carbonIntensity"`
}

// Fetch retrieves the latest carbon intensity in gCO2eq/kWh.
func (c *ElectricityMapsClient) Fetch(ctx context.Context) (float64, error) {
	if c.zone == "" {
		return 0, fmt.Errorf("electricity maps grid zone is empty")
	}

	if c.token == "" {
		return 0, fmt.Errorf("electricity maps API token is empty")
	}

	endpoint, err := url.Parse(electricityMapsEndpoint)
	if err != nil {
		return 0, fmt.Errorf("parse electricity maps endpoint: %w", err)
	}

	query := endpoint.Query()
	query.Set("zone", c.zone)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("create electricity maps request: %w", err)
	}

	req.Header.Set("auth-token", c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("electricity maps request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf(
			"electricity maps returned HTTP %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(body)),
		)
	}

	var result electricityMapsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode electricity maps response: %w", err)
	}

	if result.CarbonIntensity == nil {
		return 0, fmt.Errorf("electricity maps response is missing carbonIntensity")
	}

	value := *result.CarbonIntensity

	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, fmt.Errorf("electricity maps returned invalid carbon intensity: %v", value)
	}

	return value, nil
}

// ---------------------------------------------------------------------------
// WattTimeClient
// ---------------------------------------------------------------------------

// WattTimeClient retrieves carbon intensity from the WattTime v3 API.
//
// WattTime expresses grid emissions as MOER (Marginal Operating Emissions
// Rate) in lbs CO2/MWh. The client converts to gCO2/kWh using the factor:
//
//	lbs/MWh × 453.592 g/lb ÷ 1000 kWh/MWh = g/kWh × 0.453592
//
// WattTime Bearer tokens expire after ~30 minutes. Since the poller fires
// hourly, the client always re-authenticates at the start of each Fetch and
// automatically retries once on HTTP 401 to handle clock skew or early expiry.
type WattTimeClient struct {
	username   string
	password   string
	region     string
	httpClient *http.Client

	// mu guards cachedToken to allow safe concurrent access.
	mu          sync.Mutex
	cachedToken string
}

// NewWattTimeClient creates a WattTime API client.
// username and password are the WattTime account credentials.
// region is the WattTime balancing-authority abbreviation, e.g. "CAISO_NORTH".
func NewWattTimeClient(username, password, region string, httpClient *http.Client) *WattTimeClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &WattTimeClient{
		username:   strings.TrimSpace(username),
		password:   strings.TrimSpace(password),
		region:     strings.TrimSpace(region),
		httpClient: httpClient,
	}
}

// Source implements Provider.
func (c *WattTimeClient) Source() string { return "watttime" }

// lbsMWhToGCO2KWh converts WattTime's native unit (lbs CO2/MWh) to gCO2/kWh.
const lbsMWhToGCO2KWh = 0.453592

type wattTimeLoginResponse struct {
	Token string `json:"token"`
}

// wattTimeDataPoint is a single time-series entry from the v3 API.
type wattTimeDataPoint struct {
	PointTime string  `json:"point_time"`
	Value     float64 `json:"value"`
}

// wattTimeV3Response is the envelope for /v3/forecast and /v3/historical.
// data[0].value is MOER in lbs CO2/MWh when units = "lbs_co2_per_mwh".
type wattTimeV3Response struct {
	Data []wattTimeDataPoint `json:"data"`
}

// login obtains a JWT from the WattTime /login endpoint.
func (c *WattTimeClient) login(ctx context.Context) (string, error) {
	loginURL := wattTimeBaseURL + "/login"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return "", fmt.Errorf("create watttime login request: %w", err)
	}

	creds := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
	req.Header.Set("Authorization", "Basic "+creds)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("watttime login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf(
			"watttime login returned HTTP %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(body)),
		)
	}

	var result wattTimeLoginResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("decode watttime login response: %w", err)
	}

	if result.Token == "" {
		return "", fmt.Errorf("watttime login response is missing token")
	}

	return result.Token, nil
}

// Fetch retrieves the current MOER from WattTime and returns it in gCO2/kWh.
// WattTime tokens expire after ~30 minutes; since the poller fires hourly the
// token is always stale by the next call. Fetch always re-authenticates at the
// start of each call. If the token is rejected mid-flight (HTTP 401), it
// re-authenticates once more and retries the signal request.
func (c *WattTimeClient) Fetch(ctx context.Context) (float64, error) {
	if c.username == "" {
		return 0, fmt.Errorf("watttime username is empty")
	}
	if c.password == "" {
		return 0, fmt.Errorf("watttime password is empty")
	}
	if c.region == "" {
		return 0, fmt.Errorf("watttime region is empty")
	}

	// Always refresh the token at the start of each fetch cycle because
	// WattTime tokens expire after ~30 minutes and the poller runs hourly.
	token, err := c.login(ctx)
	if err != nil {
		return 0, fmt.Errorf("watttime authentication failed: %w", err)
	}
	c.mu.Lock()
	c.cachedToken = token
	c.mu.Unlock()

	value, statusCode, err := c.fetchSignal(ctx, token)
	if err != nil {
		if statusCode == http.StatusUnauthorized {
			// Token was rejected despite being fresh — re-auth once and retry.
			token, loginErr := c.login(ctx)
			if loginErr != nil {
				return 0, fmt.Errorf("watttime re-authentication after 401 failed: %w", loginErr)
			}
			c.mu.Lock()
			c.cachedToken = token
			c.mu.Unlock()
			value, _, err = c.fetchSignal(ctx, token)
			if err != nil {
				return 0, err
			}
		} else {
			return 0, err
		}
	}
	return value, nil
}

// fetchSignal calls /v3/forecast with the given Bearer token and returns the
// MOER value in gCO2/kWh, the HTTP status code, and any error.
//
// /v3/forecast returns data[0].value in "lbs_co2_per_mwh" for the current
// 5-minute window — the same real-time reading as /v3/signal-index but in
// raw physical units rather than a 0-100 percentile.
func (c *WattTimeClient) fetchSignal(ctx context.Context, token string) (float64, int, error) {
	forecastURL, err := url.Parse(wattTimeBaseURL + "/v3/forecast")
	if err != nil {
		return 0, 0, fmt.Errorf("parse watttime forecast endpoint: %w", err)
	}

	q := forecastURL.Query()
	q.Set("region", c.region)
	q.Set("signal_type", "co2_moer")
	q.Set("horizon_hours", "0") // current window only
	forecastURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, forecastURL.String(), nil)
	if err != nil {
		return 0, 0, fmt.Errorf("create watttime forecast request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("watttime forecast request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, resp.StatusCode, fmt.Errorf(
			"watttime forecast returned HTTP %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(body)),
		)
	}

	var result wattTimeV3Response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return 0, resp.StatusCode, fmt.Errorf("decode watttime forecast response: %w", err)
	}

	if len(result.Data) == 0 {
		return 0, resp.StatusCode, fmt.Errorf("watttime forecast response has no data points")
	}

	moer := result.Data[0].Value
	if math.IsNaN(moer) || math.IsInf(moer, 0) || moer < 0 {
		return 0, resp.StatusCode, fmt.Errorf("watttime returned invalid MOER: %v", moer)
	}

	// Convert lbs CO2/MWh → gCO2/kWh.
	return moer * lbsMWhToGCO2KWh, resp.StatusCode, nil
}
