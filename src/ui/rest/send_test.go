package rest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainSend "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
)

type sendTextStubUsecase struct {
	domainSend.ISendUsecase
	receivedRequest domainSend.MessageRequest
	called          bool
	err             error
}

func (s *sendTextStubUsecase) SendText(_ context.Context, request domainSend.MessageRequest) (domainSend.GenericResponse, error) {
	s.called = true
	s.receivedRequest = request
	return domainSend.GenericResponse{MessageID: *request.ProviderMessageID, Status: "Message sent"}, s.err
}

func TestSendTextBindsDeterministicProviderContract(t *testing.T) {
	stub := &sendTextStubUsecase{}
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Post("/send/message", (&Send{Service: stub}).SendText)

	const providerID = "3EB0A1B2C3D4E5F6071829"
	const phone = "628123456789@s.whatsapp.net"
	const bodyText = "sensitive body"
	body := `{"phone":"` + phone + `","message":"` + bodyText + `","provider_message_id":"` + providerID + `","provider_timeout_ms":20000}`
	req := httptest.NewRequest(http.MethodPost, "/send/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	if !stub.called {
		t.Fatal("usecase SendText was not called")
	}
	if stub.receivedRequest.ProviderMessageID == nil || *stub.receivedRequest.ProviderMessageID != providerID {
		t.Fatalf("provider message id was not propagated exactly")
	}
	if stub.receivedRequest.ProviderTimeoutMS == nil || *stub.receivedRequest.ProviderTimeoutMS != 20_000 {
		t.Fatalf("provider timeout was not propagated exactly")
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	responseText := string(responseBody)
	if strings.Count(responseText, providerID) != 1 {
		t.Fatalf("provider receipt must appear exactly once in success results: %s", responseText)
	}
	if strings.Contains(responseText, phone) || strings.Contains(responseText, bodyText) {
		t.Fatalf("success response exposed destination or content: %s", responseText)
	}
}

func TestSendTextMalformedBodyFailsWithoutEchoOrUsecaseCall(t *testing.T) {
	stub := &sendTextStubUsecase{}
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Post("/send/message", (&Send{Service: stub}).SendText)

	const providerID = "3EB0A1B2C3D4E5F6071829"
	const phone = "628123456789@s.whatsapp.net"
	const bodyText = "sensitive body"
	body := `{"phone":"` + phone + `","message":"` + bodyText + `","provider_message_id":"` + providerID + `","provider_timeout_ms":"not-an-integer"}`
	req := httptest.NewRequest(http.MethodPost, "/send/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusBadRequest)
	}
	if stub.called {
		t.Fatal("malformed request reached usecase")
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	responseText := string(responseBody)
	if strings.Contains(responseText, providerID) || strings.Contains(responseText, phone) || strings.Contains(responseText, bodyText) || strings.Contains(responseText, "not-an-integer") {
		t.Fatalf("error response exposed request data: %s", responseText)
	}
}

func TestSendTextUnexpectedServiceErrorIsRedacted(t *testing.T) {
	const providerID = "3EB0A1B2C3D4E5F6071829"
	const phone = "628123456789@s.whatsapp.net"
	const bodyText = "sensitive body"
	stub := &sendTextStubUsecase{err: errors.New("upstream failed for " + providerID + " " + phone + " " + bodyText)}
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Post("/send/message", (&Send{Service: stub}).SendText)
	body := `{"phone":"` + phone + `","message":"` + bodyText + `","provider_message_id":"` + providerID + `"}`
	req := httptest.NewRequest(http.MethodPost, "/send/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusInternalServerError)
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	responseText := string(responseBody)
	for _, sensitive := range []string{providerID, phone, bodyText} {
		if strings.Contains(responseText, sensitive) {
			t.Fatalf("error response exposed %q: %s", sensitive, responseText)
		}
	}
}

// sendFileStubUsecase implements domainSend.ISendUsecase by embedding the
// interface (so unrelated methods are never invoked by these tests) while
// recording the FileRequest actually received by SendFile.
type sendFileStubUsecase struct {
	domainSend.ISendUsecase
	receivedRequest domainSend.FileRequest
	called          bool
}

func (s *sendFileStubUsecase) SendFile(_ context.Context, request domainSend.FileRequest) (domainSend.GenericResponse, error) {
	s.called = true
	s.receivedRequest = request
	return domainSend.GenericResponse{Status: "ok"}, nil
}

func newSendFileTestApp(stub *sendFileStubUsecase) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	controller := Send{Service: stub}
	app.Post("/send/file", controller.SendFile)
	return app
}

// TestSendFileJSONBodyWithFileURLDoesNotPanic is a regression test for
// https://github.com/aldinokemal/go-whatsapp-web-multidevice/issues/744:
// a JSON request carrying file_url (no multipart file part) must reach the
// usecase instead of panicking into a 500 from the unguarded FormFile error.
func TestSendFileJSONBodyWithFileURLDoesNotPanic(t *testing.T) {
	stub := &sendFileStubUsecase{}
	app := newSendFileTestApp(stub)

	body := `{"phone":"628123456789@s.whatsapp.net","file_url":"https://example.com/doc.pdf"}`
	req := httptest.NewRequest(http.MethodPost, "/send/file", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d (request should not panic into a 500)", resp.StatusCode, fiber.StatusOK)
	}
	if !stub.called {
		t.Fatalf("usecase SendFile was not called")
	}
	if stub.receivedRequest.FileURL == nil || *stub.receivedRequest.FileURL != "https://example.com/doc.pdf" {
		t.Fatalf("FileURL = %v, want https://example.com/doc.pdf", stub.receivedRequest.FileURL)
	}
	if stub.receivedRequest.File != nil {
		t.Fatalf("File = %+v, want nil since no multipart file part was sent", stub.receivedRequest.File)
	}
}

// TestSendFileMultipartStillPopulatesFile ensures the original multipart
// upload path keeps working after guarding the FormFile error.
func TestSendFileMultipartStillPopulatesFile(t *testing.T) {
	stub := &sendFileStubUsecase{}
	app := newSendFileTestApp(stub)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("phone", "628123456789@s.whatsapp.net"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	part, err := writer.CreateFormFile("file", "doc.pdf")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("%PDF-1.4 fake content")); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send/file", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	if !stub.called {
		t.Fatalf("usecase SendFile was not called")
	}
	if stub.receivedRequest.File == nil {
		t.Fatalf("File = nil, want populated multipart.FileHeader")
	}
	if stub.receivedRequest.File.Filename != "doc.pdf" {
		t.Fatalf("File.Filename = %q, want doc.pdf", stub.receivedRequest.File.Filename)
	}
}
