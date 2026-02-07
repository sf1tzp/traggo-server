package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/traggo/server/auth"
	"github.com/traggo/server/model"
	"github.com/traggo/server/test"
)

func setupTest(t *testing.T) (*test.Database, *Handler, *mux.Router) {
	db := test.InMemoryDB(t)
	handler := NewHandler(db.DB)
	router := mux.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
		})
	})
	handler.RegisterRoutes(router)
	return db, handler, router
}

func withUser(r *http.Request, user *model.User) *http.Request {
	return r.WithContext(auth.WithUser(r.Context(), user))
}

func TestListTimers_Unauthenticated(t *testing.T) {
	db, _, router := setupTest(t)
	defer db.Close()

	req := httptest.NewRequest("GET", "/api/v1/timers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestListTimers_Empty(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	db.User(5)

	req := httptest.NewRequest("GET", "/api/v1/timers", nil)
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.ListTimers(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp TimersListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Timers)
}

func TestListTimers_WithRunningTimer(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	u := db.User(5)
	u.RunningTimeSpan("2024-06-10T10:00:00+02:00").Tag("project", "traggo")

	req := httptest.NewRequest("GET", "/api/v1/timers", nil)
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.ListTimers(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp TimersListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Timers, 1)
	assert.Equal(t, "traggo", resp.Timers[0].Tags["project"])
	assert.Empty(t, resp.Timers[0].End)
}

func TestListTimers_ExcludesStoppedTimers(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	u := db.User(5)
	u.TimeSpan("2024-06-10T10:00:00+02:00", "2024-06-10T11:00:00+02:00")
	u.RunningTimeSpan("2024-06-10T12:00:00+02:00")

	req := httptest.NewRequest("GET", "/api/v1/timers", nil)
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.ListTimers(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp TimersListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Timers, 1)
}

func TestStartTimer_Unauthenticated(t *testing.T) {
	db, _, router := setupTest(t)
	defer db.Close()

	body := `{"tags":{"project":"test"}}`
	req := httptest.NewRequest("POST", "/api/v1/timers/start", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestStartTimer_WithTags(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	db.User(5)

	body := `{"tags":{"project":"traggo","bead":"bead-123"},"note":"working on feature"}`
	req := httptest.NewRequest("POST", "/api/v1/timers/start", bytes.NewBufferString(body))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.StartTimer(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp TimerResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotZero(t, resp.ID)
	assert.Equal(t, "traggo", resp.Tags["project"])
	assert.Equal(t, "bead-123", resp.Tags["bead"])
	assert.Equal(t, "working on feature", resp.Note)
	assert.NotEmpty(t, resp.Start)
	assert.Empty(t, resp.End)

	// Verify tag definitions were auto-created
	var tagDefs []model.TagDefinition
	db.Where("user_id = ?", 5).Find(&tagDefs)
	assert.Len(t, tagDefs, 2)
}

func TestStartTimer_AutoCreatesTagDefinitions(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	db.User(5)

	// No tag definitions exist yet
	var count int
	db.Model(new(model.TagDefinition)).Where("user_id = ?", 5).Count(&count)
	assert.Equal(t, 0, count)

	body := `{"tags":{"newproject":"value1"}}`
	req := httptest.NewRequest("POST", "/api/v1/timers/start", bytes.NewBufferString(body))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.StartTimer(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	// Tag definition should now exist
	db.Model(new(model.TagDefinition)).Where("user_id = ?", 5).Count(&count)
	assert.Equal(t, 1, count)

	var tagDef model.TagDefinition
	db.Where("user_id = ? AND key = ?", 5, "newproject").Find(&tagDef)
	assert.Equal(t, "#607D8B", tagDef.Color)
}

func TestStartTimer_ReusesExistingTagDefinitions(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	u := db.User(5)
	u.NewTagDefinition("project")

	body := `{"tags":{"project":"reuse-test"}}`
	req := httptest.NewRequest("POST", "/api/v1/timers/start", bytes.NewBufferString(body))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.StartTimer(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	// Still only one tag definition
	var count int
	db.Model(new(model.TagDefinition)).Where("user_id = ?", 5).Count(&count)
	assert.Equal(t, 1, count)
}

func TestStartTimer_WithCustomStartTime(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	db.User(5)

	startTime := "2024-06-10T10:00:00+02:00"
	body := fmt.Sprintf(`{"tags":{"project":"test"},"start":"%s"}`, startTime)
	req := httptest.NewRequest("POST", "/api/v1/timers/start", bytes.NewBufferString(body))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.StartTimer(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp TimerResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, startTime, resp.Start)
}

func TestStartTimer_InvalidJSON(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	db.User(5)

	req := httptest.NewRequest("POST", "/api/v1/timers/start", bytes.NewBufferString("not json"))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.StartTimer(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestStartTimer_InvalidStartTime(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	db.User(5)

	body := `{"tags":{},"start":"not-a-time"}`
	req := httptest.NewRequest("POST", "/api/v1/timers/start", bytes.NewBufferString(body))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.StartTimer(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestStartTimer_NoTags(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	db.User(5)

	body := `{"tags":{},"note":"no tags timer"}`
	req := httptest.NewRequest("POST", "/api/v1/timers/start", bytes.NewBufferString(body))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.StartTimer(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp TimerResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Tags)
	assert.Equal(t, "no tags timer", resp.Note)
}

func TestStopTimer_Success(t *testing.T) {
	db, _, router := setupTest(t)
	defer db.Close()
	u := db.User(5)
	span := u.RunningTimeSpan("2024-06-10T10:00:00+02:00")
	span.Tag("project", "traggo")

	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/timers/%d/stop", span.TimeSpan.ID), bytes.NewBufferString("{}"))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp TimerResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, span.TimeSpan.ID, resp.ID)
	assert.NotEmpty(t, resp.End)
	assert.Equal(t, "traggo", resp.Tags["project"])
}

func TestStopTimer_WithCustomEndTime(t *testing.T) {
	db, _, router := setupTest(t)
	defer db.Close()
	u := db.User(5)
	span := u.RunningTimeSpan("2024-06-10T10:00:00+02:00")

	endTime := "2024-06-10T12:00:00+02:00"
	body := fmt.Sprintf(`{"end":"%s"}`, endTime)
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/timers/%d/stop", span.TimeSpan.ID), bytes.NewBufferString(body))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp TimerResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.End)

	// Verify end time was persisted
	var updated model.TimeSpan
	db.Find(&updated, span.TimeSpan.ID)
	assert.NotNil(t, updated.EndUTC)
	expectedEnd, _ := time.Parse(time.RFC3339, endTime)
	assert.Equal(t, expectedEnd.UTC(), *updated.EndUTC)
}

func TestStopTimer_NotFound(t *testing.T) {
	db, _, router := setupTest(t)
	defer db.Close()
	db.User(5)

	req := httptest.NewRequest("POST", "/api/v1/timers/999/stop", bytes.NewBufferString("{}"))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestStopTimer_AlreadyStopped(t *testing.T) {
	db, _, router := setupTest(t)
	defer db.Close()
	u := db.User(5)
	span := u.TimeSpan("2024-06-10T10:00:00+02:00", "2024-06-10T11:00:00+02:00")

	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/timers/%d/stop", span.TimeSpan.ID), bytes.NewBufferString("{}"))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestStopTimer_Unauthenticated(t *testing.T) {
	db, _, router := setupTest(t)
	defer db.Close()

	req := httptest.NewRequest("POST", "/api/v1/timers/1/stop", bytes.NewBufferString("{}"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestStopTimer_WrongUser(t *testing.T) {
	db, _, router := setupTest(t)
	defer db.Close()
	u := db.User(5)
	db.User(6)
	span := u.RunningTimeSpan("2024-06-10T10:00:00+02:00")

	// User 6 tries to stop user 5's timer
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/timers/%d/stop", span.TimeSpan.ID), bytes.NewBufferString("{}"))
	req = withUser(req, &model.User{ID: 6})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestStopTimer_InvalidID(t *testing.T) {
	db, _, router := setupTest(t)
	defer db.Close()
	db.User(5)

	req := httptest.NewRequest("POST", "/api/v1/timers/notanumber/stop", bytes.NewBufferString("{}"))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestStopTimer_EmptyBody(t *testing.T) {
	db, _, router := setupTest(t)
	defer db.Close()
	u := db.User(5)
	span := u.RunningTimeSpan("2024-06-10T10:00:00+02:00")

	// No body at all - should default to now
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/timers/%d/stop", span.TimeSpan.ID), nil)
	req.ContentLength = 0
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp TimerResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.End)
}

func TestStartAndStopIntegration(t *testing.T) {
	db, handler, router := setupTest(t)
	defer db.Close()
	db.User(5)

	// Start a timer
	startBody := `{"tags":{"project":"integration","mode":"coding"},"note":"integration test"}`
	startReq := httptest.NewRequest("POST", "/api/v1/timers/start", bytes.NewBufferString(startBody))
	startReq = withUser(startReq, &model.User{ID: 5})
	startW := httptest.NewRecorder()
	handler.StartTimer(startW, startReq)
	require.Equal(t, http.StatusCreated, startW.Code)

	var startResp TimerResponse
	require.NoError(t, json.Unmarshal(startW.Body.Bytes(), &startResp))

	// List running timers - should have 1
	listReq := httptest.NewRequest("GET", "/api/v1/timers", nil)
	listReq = withUser(listReq, &model.User{ID: 5})
	listW := httptest.NewRecorder()
	handler.ListTimers(listW, listReq)
	require.Equal(t, http.StatusOK, listW.Code)

	var listResp TimersListResponse
	require.NoError(t, json.Unmarshal(listW.Body.Bytes(), &listResp))
	require.Len(t, listResp.Timers, 1)

	// Stop the timer
	stopReq := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/timers/%d/stop", startResp.ID), bytes.NewBufferString("{}"))
	stopReq = withUser(stopReq, &model.User{ID: 5})
	stopW := httptest.NewRecorder()
	router.ServeHTTP(stopW, stopReq)
	require.Equal(t, http.StatusOK, stopW.Code)

	// List running timers again - should be empty
	listReq2 := httptest.NewRequest("GET", "/api/v1/timers", nil)
	listReq2 = withUser(listReq2, &model.User{ID: 5})
	listW2 := httptest.NewRecorder()
	handler.ListTimers(listW2, listReq2)
	require.Equal(t, http.StatusOK, listW2.Code)

	var listResp2 TimersListResponse
	require.NoError(t, json.Unmarshal(listW2.Body.Bytes(), &listResp2))
	assert.Empty(t, listResp2.Timers)
}

func TestTagKeyNormalization(t *testing.T) {
	db, handler, _ := setupTest(t)
	defer db.Close()
	db.User(5)

	body := `{"tags":{"  Project  ":"test"}}`
	req := httptest.NewRequest("POST", "/api/v1/timers/start", bytes.NewBufferString(body))
	req = withUser(req, &model.User{ID: 5})
	w := httptest.NewRecorder()
	handler.StartTimer(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp TimerResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "test", resp.Tags["project"])
}
