package wait0

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"
)

const invalidationWriteScope = "invalidation:write"
const invalidationEndpointPath = "/wait0/invalidate"

type invalidateRequest struct {
	Paths []string `json:"paths"`
	Tags  []string `json:"tags"`
}

type invalidateJob struct {
	RequestID string
	ActorID   string

	Paths []string
	Tags  []string

	ReceivedAt time.Time
	RemoteAddr string
	UserAgent  string
}

func (s *Service) handleInvalidateAPI(w http.ResponseWriter, r *http.Request) {
	if !s.invCfg.Enabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": "method not allowed",
		})
		return
	}
	ct := strings.TrimSpace(r.Header.Get("Content-Type"))
	if ct == "" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "content-type must be application/json"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "content-type must be application/json"})
		return
	}

	actor, ok := authenticateBearerToken(r.Header.Get("Authorization"), s.invTokens)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if !isAuthorizedForScope(actor, invalidationWriteScope) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	if s.invQueue == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "invalidation queue is unavailable"})
		return
	}

	body := http.MaxBytesReader(w, r.Body, int64(s.invCfg.MaxBodyBytes))
	defer body.Close()

	var req invalidateRequest
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON body must contain a single object"})
		return
	}

	normalizedPaths, err := normalizeInvalidatePaths(req.Paths)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	normalizedTags, err := normalizeInvalidateTags(req.Tags)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(normalizedPaths) == 0 && len(normalizedTags) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "at least one non-empty path or tag is required"})
		return
	}

	if len(normalizedPaths) > s.invCfg.MaxPaths {
		if s.invCfg.HardLimits {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "paths limit exceeded"})
			return
		}
		log.Printf("invalidation request over soft path limit: actor=%q requestPaths=%d maxPaths=%d", actor.ID, len(normalizedPaths), s.invCfg.MaxPaths)
	}
	if len(normalizedTags) > s.invCfg.MaxTags {
		if s.invCfg.HardLimits {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tags limit exceeded"})
			return
		}
		log.Printf("invalidation request over soft tag limit: actor=%q requestTags=%d maxTags=%d", actor.ID, len(normalizedTags), s.invCfg.MaxTags)
	}

	job := invalidateJob{
		RequestID:  "inv_" + randomString(16),
		ActorID:    actor.ID,
		Paths:      normalizedPaths,
		Tags:       normalizedTags,
		ReceivedAt: time.Now().UTC(),
		RemoteAddr: strings.TrimSpace(r.RemoteAddr),
		UserAgent:  strings.TrimSpace(r.UserAgent()),
	}

	select {
	case s.invQueue <- job:
		log.Printf(
			"invalidation accepted: request_id=%q actor=%q remote=%q paths=%d tags=%d",
			job.RequestID,
			job.ActorID,
			job.RemoteAddr,
			len(job.Paths),
			len(job.Tags),
		)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":     "accepted",
			"request_id": job.RequestID,
			"received": map[string]int{
				"paths": len(job.Paths),
				"tags":  len(job.Tags),
			},
		})
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "invalidation queue is full, retry later"})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
