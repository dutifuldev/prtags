package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dutifuldev/prtags/internal/core"
	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/jsend"
	"github.com/dutifuldev/prtags/internal/permissions"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

type Server struct {
	echo              *echo.Echo
	service           *core.Service
	db                *gorm.DB
	allowUnauthWrites bool
}

func NewServer(db *gorm.DB, service *core.Service, allowUnauthWrites bool) *Server {
	s := &Server{
		echo:              echo.New(),
		service:           service,
		db:                db,
		allowUnauthWrites: allowUnauthWrites,
	}
	s.registerRoutes()
	return s
}

func (s *Server) Echo() *echo.Echo {
	return s.echo
}

func (s *Server) registerRoutes() {
	s.echo.Use(s.requestMetricsMiddleware)

	s.echo.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, jsend.Success(map[string]any{"status": "ok"}))
	})
	s.echo.GET("/readyz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, jsend.Success(map[string]any{"status": "ready"}))
	})

	s.echo.POST("/v1/repos/:owner/:repo/fields", s.handleCreateField)
	s.echo.GET("/v1/repos/:owner/:repo/fields", s.handleListFields)
	s.echo.PATCH("/v1/repos/:owner/:repo/fields/:id", s.handleUpdateField)
	s.echo.POST("/v1/repos/:owner/:repo/fields/:id/archive", s.handleArchiveField)
	s.echo.POST("/v1/repos/:owner/:repo/fields/import", s.handleImportFields)
	s.echo.GET("/v1/repos/:owner/:repo/fields/export", s.handleExportFields)

	s.echo.POST("/v1/repos/:owner/:repo/groups", s.handleCreateGroup)
	s.echo.GET("/v1/repos/:owner/:repo/groups", s.handleListGroups)
	s.echo.GET("/v1/repos/:owner/:repo/group-comment-sync-targets", s.handleListGroupCommentSyncTargets)
	s.echo.GET("/v1/groups/:id", s.handleGetGroup)
	s.echo.PATCH("/v1/groups/:id", s.handleUpdateGroup)
	s.echo.POST("/v1/groups/:id/members", s.handleAddGroupMember)
	s.echo.DELETE("/v1/groups/:id/members/:member_id", s.handleRemoveGroupMember)
	s.echo.POST("/v1/groups/:id/sync-comments", s.handleSyncGroupComments)

	s.echo.POST("/v1/repos/:owner/:repo/pulls/:number/annotations", s.handleSetPullRequestAnnotations)
	s.echo.GET("/v1/repos/:owner/:repo/pulls/:number/annotations", s.handleGetPullRequestAnnotations)
	s.echo.POST("/v1/repos/:owner/:repo/issues/:number/annotations", s.handleSetIssueAnnotations)
	s.echo.GET("/v1/repos/:owner/:repo/issues/:number/annotations", s.handleGetIssueAnnotations)
	s.echo.POST("/v1/groups/:id/annotations", s.handleSetGroupAnnotations)
	s.echo.GET("/v1/groups/:id/annotations", s.handleGetGroupAnnotations)
	s.echo.GET("/v1/repos/:owner/:repo/targets", s.handleFilterTargets)

	s.echo.POST("/v1/repos/:owner/:repo/search/text", s.handleSearchText)
	s.echo.POST("/v1/repos/:owner/:repo/search/similar", s.handleSearchSimilar)
}

func (s *Server) requestMetricsMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		metrics := database.NewQueryMetrics()
		request := c.Request()
		c.SetRequest(request.WithContext(database.WithQueryMetrics(request.Context(), metrics)))

		err := next(c)
		if shouldLogRequestMetrics(c) {
			snapshot := metrics.Snapshot()
			slog.Info("http request",
				"method", request.Method,
				"route", requestMetricRoute(c, request),
				"status", responseStatus(c, err),
				"duration_ms", durationMillis(time.Since(start)),
				"db_query_count", snapshot.QueryCount,
				"db_duration_ms", durationMillis(snapshot.QueryDuration),
				"db_slowest_ms", durationMillis(snapshot.SlowestQuery),
				"steps", snapshot.Steps,
			)
		}
		return err
	}
}

func shouldLogRequestMetrics(c echo.Context) bool {
	path := c.Request().URL.Path
	return path != "/healthz" && path != "/readyz"
}

func requestMetricRoute(c echo.Context, request *http.Request) string {
	if route := c.Path(); route != "" {
		return route
	}
	return request.URL.Path
}

func responseStatus(c echo.Context, err error) int {
	if c.Response().Status != 0 {
		return c.Response().Status
	}
	if err != nil {
		var httpError *echo.HTTPError
		if errors.As(err, &httpError) {
			return httpError.Code
		}
		return http.StatusInternalServerError
	}
	return http.StatusOK
}

func durationMillis(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}

func (s *Server) handleCreateField(c echo.Context) error {
	var input core.FieldDefinitionInput
	if err := decodeJSONBody(c, &input); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	field, err := s.service.CreateFieldDefinition(c.Request().Context(), s.actorFromRequest(c), c.Param("owner"), c.Param("repo"), input, c.Request().Header.Get("Idempotency-Key"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusCreated, jsend.Success(field))
}

func (s *Server) handleListFields(c echo.Context) error {
	fields, err := s.service.ListFieldDefinitions(c.Request().Context(), c.Param("owner"), c.Param("repo"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(fields))
}

func (s *Server) handleUpdateField(c echo.Context) error {
	fieldID, err := parseUintParam(c.Param("id"))
	if err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid field id"})
	}
	var input core.FieldDefinitionPatchInput
	if err := decodeJSONBody(c, &input); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	field, err := s.service.UpdateFieldDefinition(c.Request().Context(), s.actorFromRequest(c), c.Param("owner"), c.Param("repo"), fieldID, input, c.Request().Header.Get("Idempotency-Key"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(field))
}

func (s *Server) handleArchiveField(c echo.Context) error {
	fieldID, err := parseUintParam(c.Param("id"))
	if err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid field id"})
	}
	var input struct {
		ExpectedRowVersion *int `json:"expected_row_version"`
	}
	if err := decodeJSONBody(c, &input); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	field, err := s.service.ArchiveFieldDefinition(c.Request().Context(), s.actorFromRequest(c), c.Param("owner"), c.Param("repo"), fieldID, input.ExpectedRowVersion, c.Request().Header.Get("Idempotency-Key"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(field))
}

func (s *Server) handleImportFields(c echo.Context) error {
	var manifest core.Manifest
	if err := decodeJSONBody(c, &manifest); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	fields, err := s.service.ImportManifest(c.Request().Context(), s.actorFromRequest(c), c.Param("owner"), c.Param("repo"), manifest, c.Request().Header.Get("Idempotency-Key"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(fields))
}

func (s *Server) handleExportFields(c echo.Context) error {
	manifest, err := s.service.ExportManifest(c.Request().Context(), c.Param("owner"), c.Param("repo"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(manifest))
}

func (s *Server) handleCreateGroup(c echo.Context) error {
	var input core.GroupInput
	if err := decodeJSONBody(c, &input); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	group, err := s.service.CreateGroup(c.Request().Context(), s.actorFromRequest(c), c.Param("owner"), c.Param("repo"), input, c.Request().Header.Get("Idempotency-Key"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusCreated, jsend.Success(group))
}

func (s *Server) handleListGroups(c echo.Context) error {
	groups, err := s.service.ListGroups(c.Request().Context(), c.Param("owner"), c.Param("repo"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(groups))
}

func (s *Server) handleListGroupCommentSyncTargets(c echo.Context) error {
	targets, err := s.service.ListGroupCommentSyncTargets(c.Request().Context(), s.actorFromRequest(c), c.Param("owner"), c.Param("repo"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(targets))
}

func (s *Server) handleGetGroup(c echo.Context) error {
	group, members, annotations, err := s.service.GetGroup(c.Request().Context(), c.Param("id"), core.GetGroupOptions{
		IncludeMetadata: includeMetadata(c),
	})
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(map[string]any{
		"group":       group,
		"members":     members,
		"annotations": annotations,
	}))
}

func (s *Server) handleUpdateGroup(c echo.Context) error {
	var input core.GroupPatchInput
	if err := decodeJSONBody(c, &input); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	group, err := s.service.UpdateGroup(c.Request().Context(), s.actorFromRequest(c), c.Param("id"), input, c.Request().Header.Get("Idempotency-Key"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(group))
}

func (s *Server) handleAddGroupMember(c echo.Context) error {
	var input struct {
		ObjectType   string `json:"object_type"`
		ObjectNumber int    `json:"object_number"`
	}
	if err := decodeJSONBody(c, &input); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	member, err := s.service.AddGroupMember(c.Request().Context(), s.actorFromRequest(c), c.Param("id"), input.ObjectType, input.ObjectNumber, c.Request().Header.Get("Idempotency-Key"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusCreated, jsend.Success(member))
}

func (s *Server) handleRemoveGroupMember(c echo.Context) error {
	memberID, err := parseUintParam(c.Param("member_id"))
	if err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid member id"})
	}
	if err := s.service.RemoveGroupMember(c.Request().Context(), s.actorFromRequest(c), c.Param("id"), memberID, c.Request().Header.Get("Idempotency-Key")); err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(map[string]any{"removed": true}))
}

func (s *Server) handleSyncGroupComments(c echo.Context) error {
	result, err := s.service.SyncGroupComments(c.Request().Context(), s.actorFromRequest(c), c.Param("id"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(result))
}

func (s *Server) handleSetPullRequestAnnotations(c echo.Context) error {
	return s.handleSetObjectAnnotations(c, "pull_request")
}

func (s *Server) handleGetPullRequestAnnotations(c echo.Context) error {
	return s.handleGetObjectAnnotations(c, "pull_request")
}

func (s *Server) handleSetIssueAnnotations(c echo.Context) error {
	return s.handleSetObjectAnnotations(c, "issue")
}

func (s *Server) handleGetIssueAnnotations(c echo.Context) error {
	return s.handleGetObjectAnnotations(c, "issue")
}

func (s *Server) handleSetObjectAnnotations(c echo.Context, targetType string) error {
	number, err := parseIntParam(c.Param("number"))
	if err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid object number"})
	}
	values := map[string]any{}
	if err := decodeJSONBody(c, &values); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	result, err := s.service.SetAnnotations(c.Request().Context(), s.actorFromRequest(c), c.Param("owner"), c.Param("repo"), targetType, number, nil, values, c.Request().Header.Get("Idempotency-Key"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(result))
}

func (s *Server) handleGetObjectAnnotations(c echo.Context, targetType string) error {
	number, err := parseIntParam(c.Param("number"))
	if err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid object number"})
	}
	result, err := s.service.GetAnnotations(c.Request().Context(), c.Param("owner"), c.Param("repo"), targetType, number, nil)
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(result))
}

func (s *Server) handleSetGroupAnnotations(c echo.Context) error {
	group, err := s.loadGroupByPublicID(c)
	if err != nil {
		return s.renderError(c, err)
	}
	values := map[string]any{}
	if err := decodeJSONBody(c, &values); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	result, err := s.service.SetAnnotations(c.Request().Context(), s.actorFromRequest(c), group.RepositoryOwner, group.RepositoryName, "group", 0, &group.ID, values, c.Request().Header.Get("Idempotency-Key"))
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(result))
}

func (s *Server) handleGetGroupAnnotations(c echo.Context) error {
	group, err := s.loadGroupByPublicID(c)
	if err != nil {
		return s.renderError(c, err)
	}
	result, err := s.service.GetAnnotations(c.Request().Context(), group.RepositoryOwner, group.RepositoryName, "group", 0, &group.ID)
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(result))
}

func (s *Server) handleFilterTargets(c echo.Context) error {
	targetType := strings.TrimSpace(c.QueryParam("target_type"))
	fieldName := strings.TrimSpace(c.QueryParam("field"))
	value := c.QueryParam("value")
	results, err := s.service.FilterTargets(c.Request().Context(), c.Param("owner"), c.Param("repo"), targetType, fieldName, value)
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(results))
}

func (s *Server) handleSearchText(c echo.Context) error {
	var input struct {
		Query       string   `json:"query"`
		TargetTypes []string `json:"target_types"`
		Limit       int      `json:"limit"`
	}
	if err := decodeJSONBody(c, &input); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	results, err := s.service.SearchText(c.Request().Context(), c.Param("owner"), c.Param("repo"), input.Query, input.TargetTypes, input.Limit)
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(results))
}

func (s *Server) handleSearchSimilar(c echo.Context) error {
	var input struct {
		Query       string   `json:"query"`
		TargetTypes []string `json:"target_types"`
		Limit       int      `json:"limit"`
	}
	if err := decodeJSONBody(c, &input); err != nil {
		return s.renderError(c, &core.FailError{StatusCode: 400, Message: "invalid request body"})
	}
	results, err := s.service.SearchSimilar(c.Request().Context(), c.Param("owner"), c.Param("repo"), input.Query, input.TargetTypes, input.Limit)
	if err != nil {
		return s.renderError(c, err)
	}
	return c.JSON(http.StatusOK, jsend.Success(results))
}

func (s *Server) renderError(c echo.Context, err error) error {
	var fail *core.FailError
	if errors.As(err, &fail) {
		return c.JSON(fail.StatusCode, jsend.Fail(map[string]any{
			"message": fail.Message,
			"details": fail.Data,
		}))
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.JSON(http.StatusNotFound, jsend.Fail(map[string]any{"message": "not found"}))
	}
	return c.JSON(http.StatusInternalServerError, jsend.Error("internal error", map[string]any{"details": err.Error()}))
}

func (s *Server) actorFromRequest(c echo.Context) permissions.Actor {
	if s.allowUnauthWrites {
		id := strings.TrimSpace(c.Request().Header.Get("X-Actor"))
		if id == "" {
			id = "local-dev"
		}
		return permissions.Actor{Type: "user", ID: id}
	}

	token := strings.TrimSpace(strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer "))
	digest := sha256.Sum256([]byte(token))
	return permissions.Actor{
		Type:  "github",
		ID:    hex.EncodeToString(digest[:8]),
		Token: token,
	}
}

func parseUintParam(value string) (uint, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return uint(parsed), err
}

func parseIntParam(value string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(value))
}

func includeMetadata(c echo.Context) bool {
	for _, value := range strings.Split(c.QueryParam("include"), ",") {
		if strings.EqualFold(strings.TrimSpace(value), "metadata") {
			return true
		}
	}
	return false
}

func decodeJSONBody(c echo.Context, target any) error {
	err := json.NewDecoder(c.Request().Body).Decode(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (s *Server) loadGroupByPublicID(c echo.Context) (database.Group, error) {
	var group database.Group
	err := s.db.WithContext(c.Request().Context()).
		Where("public_id = ?", strings.TrimSpace(c.Param("id"))).
		First(&group).Error
	return group, err
}
