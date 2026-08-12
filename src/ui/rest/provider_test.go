package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainProvider "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/provider"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

type providerLookupStub struct {
	result domainProvider.MessageLookup
	err    error
	gotID  string
}

func (s *providerLookupStub) LookupMessage(_ context.Context, providerMessageID string) (domainProvider.MessageLookup, error) {
	s.gotID = providerMessageID
	return s.result, s.err
}

func newProviderLookupTestApp(service domainProvider.IMessageLookupUsecase) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	controller := NewProvider(service)
	app.Post(ProviderLookupPath, middleware.ProviderLookupBoundary(), controller.LookupMessage)
	return app
}

func providerLookupRequest(providerID string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/provider/messages/lookup", strings.NewReader(`{"provider_message_id":"`+providerID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestProviderLookupHTTPReturnsOnlyRedactedPresentReceipt(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	stub := &providerLookupStub{result: domainProvider.MessageLookup{
		Status:  domainProvider.MessagePresent,
		Receipt: &domainProvider.MessageReceipt{ProviderMessageID: providerID},
	}}
	resp, err := newProviderLookupTestApp(stub).Test(providerLookupRequest(providerID))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK || stub.gotID != providerID {
		t.Fatalf("status/id = %d/%q", resp.StatusCode, stub.gotID)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	if strings.Count(text, providerID) != 1 || !strings.Contains(text, `"status":"PRESENT"`) {
		t.Fatalf("response = %s, want PRESENT with one opaque receipt", text)
	}
	for _, forbidden := range []string{"chat_jid", "device_id", "sender", "content", "timestamp"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("response exposed %q: %s", forbidden, text)
		}
	}
}

func TestProviderLookupHTTPUnknownDoesNotEchoReceipt(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	stub := &providerLookupStub{result: domainProvider.MessageLookup{Status: domainProvider.MessageUnknown}}
	resp, err := newProviderLookupTestApp(stub).Test(providerLookupRequest(providerID))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var payload struct {
		Code    string                       `json:"code"`
		Results domainProvider.MessageLookup `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StatusCode != http.StatusOK || payload.Code != "SUCCESS" || payload.Results.Status != domainProvider.MessageUnknown || payload.Results.Receipt != nil {
		t.Fatalf("status/payload = %d %#v", resp.StatusCode, payload)
	}
}

func TestProviderLookupHTTPUnexpectedErrorIsRedacted(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	stub := &providerLookupStub{err: errors.New("storage failed for " + providerID + " device-a@s.whatsapp.net")}
	resp, err := newProviderLookupTestApp(stub).Test(providerLookupRequest(providerID))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	text := string(body)
	for _, sensitive := range []string{providerID, "device-a@s.whatsapp.net", "storage failed"} {
		if strings.Contains(text, sensitive) {
			t.Fatalf("error response exposed %q: %s", sensitive, text)
		}
	}
}

func TestProviderLookupHTTPMalformedBodyIsRedacted(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	stub := &providerLookupStub{}
	request := httptest.NewRequest(http.MethodPost, "/provider/messages/lookup", strings.NewReader(`{"provider_message_id":"`+providerID))
	request.Header.Set("Content-Type", "application/json")
	resp, err := newProviderLookupTestApp(stub).Test(request)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest || stub.gotID != "" || strings.Contains(string(body), providerID) {
		t.Fatalf("malformed status/id/body = %d/%q/%s", resp.StatusCode, stub.gotID, body)
	}
}
