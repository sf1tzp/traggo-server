package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/jinzhu/gorm"
	"github.com/rs/zerolog/log"
	"github.com/traggo/server/auth"
	"github.com/traggo/server/model"
)

// TimerResponse is the JSON response for a timer/timespan.
type TimerResponse struct {
	ID    int               `json:"id"`
	Start string            `json:"start"`
	End   string            `json:"end,omitempty"`
	Tags  map[string]string `json:"tags"`
	Note  string            `json:"note"`
}

// StartRequest is the JSON body for starting a timer.
type StartRequest struct {
	Tags map[string]string `json:"tags"`
	Note string            `json:"note"`
	// Start time in RFC3339 format. Defaults to now.
	Start string `json:"start,omitempty"`
}

// StopRequest is the JSON body for stopping a timer.
type StopRequest struct {
	// End time in RFC3339 format. Defaults to now.
	End string `json:"end,omitempty"`
}

// TimersListResponse is the JSON response for listing timers.
type TimersListResponse struct {
	Timers []TimerResponse `json:"timers"`
}

// Handler provides REST API endpoints for agent tool calls.
type Handler struct {
	DB *gorm.DB
}

// NewHandler creates a new API handler.
func NewHandler(db *gorm.DB) *Handler {
	return &Handler{DB: db}
}

// RegisterRoutes registers the REST API routes on the given router.
func (h *Handler) RegisterRoutes(router *mux.Router) {
	sub := router.PathPrefix("/api/v1/timers").Subrouter()
	sub.HandleFunc("", h.ListTimers).Methods("GET")
	sub.HandleFunc("/start", h.StartTimer).Methods("POST")
	sub.HandleFunc("/{id}/stop", h.StopTimer).Methods("POST")
}

// ListTimers returns all running timers for the authenticated user.
func (h *Handler) ListTimers(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var timeSpans []model.TimeSpan
	h.DB.Preload("Tags").
		Where("user_id = ?", user.ID).
		Where("end_user_time is null").
		Order("start_user_time DESC").
		Find(&timeSpans)

	timers := make([]TimerResponse, 0, len(timeSpans))
	for _, span := range timeSpans {
		timers = append(timers, toTimerResponse(span))
	}

	writeJSON(w, http.StatusOK, TimersListResponse{Timers: timers})
}

// StartTimer starts a new timer with the given tags.
func (h *Handler) StartTimer(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	now := time.Now()
	startTime := now
	if req.Start != "" {
		parsed, err := time.Parse(time.RFC3339, req.Start)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid start time: "+err.Error())
			return
		}
		startTime = parsed
	}

	_, offset := startTime.Zone()

	tags, err := h.ensureTagDefinitions(user.ID, req.Tags)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to ensure tags: "+err.Error())
		return
	}

	span := model.TimeSpan{
		StartUserTime: omitTimeZone(startTime),
		StartUTC:      startTime.UTC(),
		OffsetUTC:     offset,
		UserID:        user.ID,
		Tags:          tags,
		Note:          req.Note,
	}

	if err := h.DB.Create(&span).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create timer: "+err.Error())
		return
	}

	log.Info().Int("id", span.ID).Int("userId", user.ID).Msg("Timer started via API")
	writeJSON(w, http.StatusCreated, toTimerResponse(span))
}

// StopTimer stops a running timer by ID.
func (h *Handler) StopTimer(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid timer ID")
		return
	}

	var req StopRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}

	now := time.Now()
	endTime := now
	if req.End != "" {
		parsed, err := time.Parse(time.RFC3339, req.End)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid end time: "+err.Error())
			return
		}
		endTime = parsed
	}

	span := &model.TimeSpan{ID: id}
	if h.DB.Preload("Tags").Where("user_id = ?", user.ID).Find(span).RecordNotFound() {
		writeError(w, http.StatusNotFound, fmt.Sprintf("timer with id %d not found", id))
		return
	}

	if span.EndUTC != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("timer with id %d is already stopped", id))
		return
	}

	endUTC := endTime.UTC()
	span.EndUTC = &endUTC
	endUser := omitTimeZone(endTime)
	span.EndUserTime = &endUser

	if err := h.DB.Save(span).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "failed to stop timer: "+err.Error())
		return
	}

	log.Info().Int("id", span.ID).Int("userId", user.ID).Msg("Timer stopped via API")
	writeJSON(w, http.StatusOK, toTimerResponse(*span))
}

// ensureTagDefinitions auto-creates tag definitions that don't exist yet and
// returns the corresponding TimeSpanTag slice.
func (h *Handler) ensureTagDefinitions(userID int, tags map[string]string) ([]model.TimeSpanTag, error) {
	result := make([]model.TimeSpanTag, 0, len(tags))

	for key, value := range tags {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if normalizedKey == "" {
			continue
		}

		// Auto-create tag definition if it doesn't exist.
		if h.DB.Where("key = ? AND user_id = ?", normalizedKey, userID).Find(new(model.TagDefinition)).RecordNotFound() {
			def := &model.TagDefinition{
				Key:    normalizedKey,
				UserID: userID,
				Color:  "#607D8B", // default blue-grey
			}
			if err := h.DB.Create(def).Error; err != nil {
				return nil, fmt.Errorf("failed to create tag definition '%s': %w", normalizedKey, err)
			}
			log.Info().Str("key", normalizedKey).Int("userId", userID).Msg("Auto-created tag definition via API")
		}

		result = append(result, model.TimeSpanTag{
			Key:         normalizedKey,
			StringValue: value,
		})
	}

	return result, nil
}

func toTimerResponse(span model.TimeSpan) TimerResponse {
	location := time.FixedZone("offset", span.OffsetUTC)

	resp := TimerResponse{
		ID:    span.ID,
		Start: span.StartUTC.In(location).Format(time.RFC3339),
		Tags:  make(map[string]string),
		Note:  span.Note,
	}

	if span.EndUTC != nil && !span.EndUTC.IsZero() {
		resp.End = span.EndUTC.In(location).Format(time.RFC3339)
	}

	for _, tag := range span.Tags {
		resp.Tags[tag.Key] = tag.StringValue
	}

	return resp
}

func omitTimeZone(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Error().Err(err).Msg("Failed to encode JSON response")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
