package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dutifuldev/prtags/internal/core"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestRenderErrorAndActorFromRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Actor", "tester")
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	server := &Server{}

	actor := server.actorFromRequest(c)
	require.Equal(t, "2bb80d537b1da3e3", actor.ID)
	require.Equal(t, "secret", actor.Token)
	require.NoError(t, server.renderError(c, &core.FailError{StatusCode: http.StatusBadRequest, Message: "bad input", Data: map[string]any{"field": "title"}}))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"bad input"`)

	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	require.NoError(t, server.renderError(c, core.ErrNotFound))
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	require.NoError(t, server.renderError(c, echo.NewHTTPError(http.StatusTeapot, "tea")))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestDecodeJSONBodyFailures(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"ok":`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := e.NewContext(req, httptest.NewRecorder())

	var payload map[string]any
	err := decodeJSONBody(c, &payload)
	require.Error(t, err)
}

func TestDecodeJSONBodySuccess(t *testing.T) {
	e := echo.New()
	body, err := json.Marshal(map[string]any{"ok": true})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := e.NewContext(req, httptest.NewRecorder())

	var payload map[string]any
	require.NoError(t, decodeJSONBody(c, &payload))
	require.Equal(t, true, payload["ok"])
}

func TestDecodeJSONBodyAllowsEOF(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(``))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := e.NewContext(req, httptest.NewRecorder())

	var payload struct {
		OK bool `json:"ok"`
	}
	require.NoError(t, decodeJSONBody(c, &payload))
}

func TestResponseStatusUsesWrittenOrReturnedErrorStatus(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c := e.NewContext(req, httptest.NewRecorder())

	require.Equal(t, http.StatusOK, responseStatus(c, nil))
	require.Equal(t, http.StatusTeapot, responseStatus(c, echo.NewHTTPError(http.StatusTeapot, "tea")))
	require.Equal(t, http.StatusInternalServerError, responseStatus(c, errors.New("boom")))

	c.Response().Status = http.StatusCreated
	require.Equal(t, http.StatusCreated, responseStatus(c, echo.NewHTTPError(http.StatusBadRequest, "bad")))
}
