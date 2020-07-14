package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"sync"
)

// UnauthorizedError represents the message of a HTTP 401 response
type UnauthorizedError ErrorMessage

// UnprocessableEntityError represents the generic structure of a error response
type UnprocessableEntityError struct {
	Error ErrorMessage `json:"error"`
}

// ErrorMessage is the message of an error response
type ErrorMessage struct {
	Message string `json:"message"`
}

type createHTTPClient func() *http.Client

func defaultCreateHTTPClient() *http.Client {
	return &http.Client{}
}

// Client for the Hetzner DNS API.
type Client struct {
	requestLock      sync.Mutex
	apiToken         string
	createHTTPClient createHTTPClient
}

// NewClient creates a new API Client using a given api token.
func NewClient(apiToken string) (*Client, error) {
	return &Client{apiToken: apiToken, createHTTPClient: defaultCreateHTTPClient}, nil
}

func (c *Client) doHTTPRequest(apiToken string, method string, url string, body io.Reader) (*http.Response, error) {
	client := c.createHTTPClient()
	log.Printf("[DEBUG] HTTP request to API %s %s", method, url)
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Auth-API-Token", apiToken)
	req.Header.Add("Accept", "application/json; charset=utf-8")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}

	// This lock ensures that only one request is sent to Hetzber API
	// at a time. See issue #5 for context.
	// It seems that Terraform creates multiple resources simultanously
	// and the API can't handle this right now.
	c.requestLock.Lock()

	resp, err := client.Do(req)

	c.requestLock.Unlock()

	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		unauthorizedError, err := parseUnauthorizedError(resp)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("API returned HTTP 401 Unauthorized error with message: '%s'. Double check your API key is still valid", unauthorizedError.Message)

	} else if resp.StatusCode == http.StatusUnprocessableEntity {
		unprocessableEntityError, err := parseUnprocessableEntityError(resp)
		if err != nil {
			return nil, err
		}

		return nil, fmt.Errorf("API returned HTTP 422 Unprocessable Entity error with message: '%s'", unprocessableEntityError.Error.Message)
	}
	return resp, nil
}

func parseUnprocessableEntityError(resp *http.Response) (*UnprocessableEntityError, error) {
	body, err := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()

	if err != nil {
		return nil, fmt.Errorf("Error reading HTTP response body: %e", err)
	}
	var unprocessableEntityError UnprocessableEntityError
	err = parseJSON(body, &unprocessableEntityError)
	if err != nil {
		return nil, err
	}
	return &unprocessableEntityError, nil
}

func parseUnauthorizedError(resp *http.Response) (*UnauthorizedError, error) {
	var unauthorizedError UnauthorizedError
	err := readAndParseJSONBody(resp, &unauthorizedError)
	if err != nil {
		return nil, err
	}
	return &unauthorizedError, nil
}

func (c *Client) doGetRequest(url string) (*http.Response, error) {
	return c.doHTTPRequest(c.apiToken, http.MethodGet, url, nil)
}

func (c *Client) doDeleteRequest(url string) (*http.Response, error) {
	return c.doHTTPRequest(c.apiToken, http.MethodDelete, url, nil)
}

func (c *Client) doPostRequest(url string, bodyJSON interface{}) (*http.Response, error) {
	reqJSON, err := json.Marshal(bodyJSON)
	if err != nil {
		return nil, fmt.Errorf("Error serializing JSON body %s", err)
	}
	body := bytes.NewReader(reqJSON)

	return c.doHTTPRequest(c.apiToken, http.MethodPost, url, body)
}

func (c *Client) doPutRequest(url string, bodyJSON interface{}) (*http.Response, error) {
	reqJSON, err := json.Marshal(bodyJSON)
	if err != nil {
		return nil, fmt.Errorf("Error serializing JSON body %s", err)
	}
	body := bytes.NewReader(reqJSON)

	return c.doHTTPRequest(c.apiToken, http.MethodPut, url, body)
}

func readAndParseJSONBody(resp *http.Response, respType interface{}) error {
	body, err := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()

	if err != nil {
		return fmt.Errorf("Error reading HTTP response body %s", err)
	}

	return parseJSON(body, respType)
}

func parseJSON(data []byte, respType interface{}) error {
	return json.Unmarshal(data, &respType)
}

// GetZone reads the current state of a DNS zone
func (c *Client) GetZone(id string) (*Zone, error) {
	resp, err := c.doGetRequest(fmt.Sprintf("https://dns.hetzner.com/api/v1/zones/%s", id))
	if err != nil {
		return nil, fmt.Errorf("Error getting zone %s: %s", id, err)
	}

	if resp.StatusCode == http.StatusOK {
		var response GetZoneResponse
		err = readAndParseJSONBody(resp, &response)
		if err != nil {
			return nil, err
		}
		return &response.Zone, nil
	} else if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	return nil, fmt.Errorf("Error getting Zone. HTTP status %d unhandled", resp.StatusCode)
}

// UpdateZone takes the passed state and updates the respective Zone
func (c *Client) UpdateZone(zone Zone) (*Zone, error) {
	resp, err := c.doPutRequest(fmt.Sprintf("https://dns.hetzner.com/api/v1/zones/%s", zone.ID), zone)
	if err != nil {
		return nil, fmt.Errorf("Error updating zone %s: %s", zone.ID, err)
	}

	if resp.StatusCode == http.StatusOK {
		var response ZoneResponse
		err = readAndParseJSONBody(resp, &response)
		if err != nil {
			return nil, err
		}
		return &response.Zone, nil
	}

	return nil, fmt.Errorf("Error updating Zone. HTTP status %d unhandled", resp.StatusCode)
}

// DeleteZone deletes a given DNS zone
func (c *Client) DeleteZone(id string) error {
	resp, err := c.doDeleteRequest(fmt.Sprintf("https://dns.hetzner.com/api/v1/zones/%s", id))
	if err != nil {
		return fmt.Errorf("Error deleting zone %s: %s", id, err)
	}

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("Error deleting Zone. HTTP status %d unhandled", resp.StatusCode)
}

// GetZoneByName reads the current state of a DNS zone with a given name
func (c *Client) GetZoneByName(name string) (*Zone, error) {
	resp, err := c.doGetRequest(fmt.Sprintf("https://dns.hetzner.com/api/v1/zones?name=%s", name))
	if err != nil {
		return nil, fmt.Errorf("Error getting zone %s: %s", name, err)
	}

	if resp.StatusCode == http.StatusOK {
		var response *GetZonesByNameResponse
		err = readAndParseJSONBody(resp, &response)
		if err != nil {
			return nil, err
		}

		if len(response.Zones) != 1 {
			return nil, fmt.Errorf("Error getting zone '%s'. No matching zone or multiple matching zones found", name)
		}

		return &response.Zones[0], nil
	} else if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	return nil, fmt.Errorf("Error getting Zone. HTTP status %d unhandled", resp.StatusCode)
}

// CreateZoneOpts covers all parameters used to create a new DNS zone
type CreateZoneOpts struct {
	Name string `json:"name"`
	TTL  int    `json:"ttl"`
}

// CreateZone creates a new DNS zone
func (c *Client) CreateZone(opts CreateZoneOpts) (*Zone, error) {

	if !strings.Contains(opts.Name, ".") {
		return nil, fmt.Errorf("Error creating zone. The name '%s' is not a valid domain. It must correspond to the schema <domain>.<tld>", opts.Name)
	}

	reqBody := CreateZoneRequest{Name: opts.Name, TTL: opts.TTL}
	resp, err := c.doPostRequest("https://dns.hetzner.com/api/v1/zones", reqBody)
	if err != nil {
		return nil, fmt.Errorf("Error creating zone. %s", err)
	}

	if resp.StatusCode == http.StatusOK {
		var response CreateZoneResponse
		err = readAndParseJSONBody(resp, &response)
		if err != nil {
			return nil, err
		}

		return &response.Zone, nil
	}

	return nil, fmt.Errorf("Error creating Zone. HTTP status %d unhandled", resp.StatusCode)
}

// GetRecordByName reads the current state of a DNS Record with a given name and zone id
func (c *Client) GetRecordByName(zoneID string, name string) (*Record, error) {
	resp, err := c.doGetRequest(fmt.Sprintf("https://dns.hetzner.com/api/v1/records?zone_id=%s", zoneID))
	if err != nil {
		return nil, fmt.Errorf("Error getting record %s: %s", name, err)
	}

	if resp.StatusCode == http.StatusOK {
		var response *RecordsResponse
		err = readAndParseJSONBody(resp, &response)
		if err != nil {
			return nil, err
		}

		if len(response.Records) == 0 {
			return nil, fmt.Errorf("Error getting record '%s'. It seems there are no records in zone %s at all", name, zoneID)
		}

		for _, record := range response.Records {
			if record.Name == name {
				return &record, nil
			}
		}

		return nil, fmt.Errorf("Error getting record '%s'. There are records in zone %s, but %s isn't included", name, zoneID, name)
	}

	return nil, fmt.Errorf("Error getting Record. HTTP status %d unhandled", resp.StatusCode)
}

// GetRecord reads the current state of a DNS Record
func (c *Client) GetRecord(recordID string) (*Record, error) {
	resp, err := c.doGetRequest(fmt.Sprintf("https://dns.hetzner.com/api/v1/records/%s", recordID))
	if err != nil {
		return nil, fmt.Errorf("Error getting record %s: %s", recordID, err)
	}

	if resp.StatusCode == http.StatusOK {
		var response *RecordResponse
		err = readAndParseJSONBody(resp, &response)
		if err != nil {
			return nil, fmt.Errorf("Error Reading json response of get record %s request: %s", recordID, err)
		}

		return &response.Record, nil
	} else if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	return nil, fmt.Errorf("Error getting Record. HTTP status %d unhandled", resp.StatusCode)
}

// CreateRecordOpts covers all parameters used to create a new DNS record
type CreateRecordOpts struct {
	ZoneID string `json:"zone_id"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	TTL    int    `json:"ttl"`
}

// CreateRecord create a new DNS records
func (c *Client) CreateRecord(opts CreateRecordOpts) (*Record, error) {
	reqBody := CreateRecordRequest{ZoneID: opts.ZoneID, Name: opts.Name, TTL: opts.TTL, Type: opts.Type, Value: opts.Value}
	resp, err := c.doPostRequest("https://dns.hetzner.com/api/v1/records", reqBody)
	if err != nil {
		return nil, fmt.Errorf("Error creating record %s: %s", opts.Name, err)
	}

	if resp.StatusCode == http.StatusOK {
		var response RecordResponse
		err = readAndParseJSONBody(resp, &response)
		if err != nil {
			return nil, err
		}

		return &response.Record, nil
	}

	return nil, fmt.Errorf("Error creating Record. HTTP status %d unhandled", resp.StatusCode)
}

// DeleteRecord deletes a given record
func (c *Client) DeleteRecord(id string) error {
	resp, err := c.doDeleteRequest(fmt.Sprintf("https://dns.hetzner.com/api/v1/records/%s", id))
	if err != nil {
		return fmt.Errorf("Error deleting zone %s: %s", id, err)
	}

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("Error deleting Record. HTTP status %d unhandled", resp.StatusCode)
}

// UpdateRecord create a new DNS records
func (c *Client) UpdateRecord(record Record) (*Record, error) {
	resp, err := c.doPutRequest(fmt.Sprintf("https://dns.hetzner.com/api/v1/records/%s", record.ID), record)
	if err != nil {
		return nil, fmt.Errorf("Error updating record %s: %s", record.ID, err)
	}

	if resp.StatusCode == http.StatusOK {
		var response RecordResponse
		err = readAndParseJSONBody(resp, &response)
		if err != nil {
			return nil, err
		}

		return &response.Record, nil
	}

	return nil, fmt.Errorf("Error creating Record. HTTP status %d unhandled", resp.StatusCode)
}
