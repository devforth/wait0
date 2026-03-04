package invalidation

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"wait0/internal/wait0/auth"
)

const WriteScope = "invalidation:write"
const EndpointPath = "/wait0/invalidate"

type Config struct {
	Enabled bool

	QueueSize         int
	WorkerConcurrency int
	MaxBodyBytes      int
	MaxPaths          int
	MaxTags           int
	HardLimits        bool
}

type Runtime interface {
	CachedKeys() []string
	KeyTags(key string) []string
	HasKey(key string) bool
	DeleteKey(key string)
	RecrawlKey(ctx context.Context, key string) string
}

type request struct {
	Paths []string `json:"paths"`
	Tags  []string `json:"tags"`
}

type Job struct {
	RequestID string
	ActorID   string

	Paths []string
	Tags  []string

	ReceivedAt time.Time
	RemoteAddr string
	UserAgent  string
}

type Controller struct {
	cfg   Config
	authn *auth.Authenticator
	rt    Runtime

	stop <-chan struct{}
	wg   *sync.WaitGroup

	queue chan Job
}

func NewController(cfg Config, authn *auth.Authenticator, rt Runtime, stop <-chan struct{}, wg *sync.WaitGroup) *Controller {
	c := &Controller{
		cfg:   cfg,
		authn: authn,
		rt:    rt,
		stop:  stop,
		wg:    wg,
	}
	if !cfg.Enabled {
		return c
	}
	if c.cfg.QueueSize <= 0 {
		c.cfg.QueueSize = 128
	}
	c.queue = make(chan Job, c.cfg.QueueSize)
	c.startWorkers()
	return c
}

func (c *Controller) Handle(w http.ResponseWriter, r *http.Request) {
	if !c.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
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

	actor, ok := c.authn.AuthenticateBearer(r.Header.Get("Authorization"))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if !auth.AuthorizedForScope(actor, WriteScope) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	if c.queue == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "invalidation queue is unavailable"})
		return
	}

	body := http.MaxBytesReader(w, r.Body, int64(c.cfg.MaxBodyBytes))
	defer body.Close()

	var req request
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

	normalizedPaths, err := NormalizePaths(req.Paths)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	normalizedTags, err := NormalizeTags(req.Tags)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(normalizedPaths) == 0 && len(normalizedTags) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "at least one non-empty path or tag is required"})
		return
	}

	if len(normalizedPaths) > c.cfg.MaxPaths {
		if c.cfg.HardLimits {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "paths limit exceeded"})
			return
		}
		log.Printf("invalidation request over soft path limit: actor=%q requestPaths=%d maxPaths=%d", actor.ID, len(normalizedPaths), c.cfg.MaxPaths)
	}
	if len(normalizedTags) > c.cfg.MaxTags {
		if c.cfg.HardLimits {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tags limit exceeded"})
			return
		}
		log.Printf("invalidation request over soft tag limit: actor=%q requestTags=%d maxTags=%d", actor.ID, len(normalizedTags), c.cfg.MaxTags)
	}

	job := Job{
		RequestID:  "inv_" + randomString(16),
		ActorID:    actor.ID,
		Paths:      normalizedPaths,
		Tags:       normalizedTags,
		ReceivedAt: time.Now().UTC(),
		RemoteAddr: strings.TrimSpace(r.RemoteAddr),
		UserAgent:  strings.TrimSpace(r.UserAgent()),
	}

	select {
	case c.queue <- job:
		log.Printf("invalidation accepted: request_id=%q actor=%q remote=%q paths=%d tags=%d", job.RequestID, job.ActorID, job.RemoteAddr, len(job.Paths), len(job.Tags))
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

func (c *Controller) startWorkers() {
	if c.queue == nil || c.cfg.WorkerConcurrency <= 0 {
		return
	}
	for i := 0; i < c.cfg.WorkerConcurrency; i++ {
		if c.wg != nil {
			c.wg.Add(1)
		}
		go func(workerID int) {
			if c.wg != nil {
				defer c.wg.Done()
			}
			c.workerLoop(workerID)
		}(i + 1)
	}
}

func (c *Controller) workerLoop(workerID int) {
	for {
		select {
		case <-c.stop:
			return
		case job := <-c.queue:
			c.processJob(workerID, job)
		}
	}
}

func (c *Controller) processJob(workerID int, job Job) {
	start := time.Now()
	keys := make(map[string]struct{}, len(job.Paths))
	for _, p := range job.Paths {
		keys[p] = struct{}{}
	}
	if len(job.Tags) > 0 {
		tagSet := make(map[string]struct{}, len(job.Tags))
		for _, tag := range job.Tags {
			tagSet[tag] = struct{}{}
		}
		for _, k := range c.resolveKeysByTags(tagSet) {
			keys[k] = struct{}{}
		}
	}

	resolved := make([]string, 0, len(keys))
	for k := range keys {
		resolved = append(resolved, k)
	}

	invalidated := 0
	for _, key := range resolved {
		had := c.rt.HasKey(key)
		c.rt.DeleteKey(key)
		if had {
			invalidated++
		}
	}

	recrawled := 0
	recrawlErrs := 0
	semSize := c.cfg.WorkerConcurrency
	if semSize <= 0 {
		semSize = 1
	}
	sem := make(chan struct{}, semSize)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, key := range resolved {
		wg.Add(1)
		sem <- struct{}{}
		go func(k string) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			kind := c.rt.RecrawlKey(ctx, k)
			cancel()
			mu.Lock()
			if kind == "updated" || kind == "unchanged" {
				recrawled++
			}
			if kind == "error" {
				recrawlErrs++
			}
			mu.Unlock()
		}(key)
	}
	wg.Wait()

	log.Printf(
		"invalidation completed: request_id=%q actor=%q worker=%d requested_paths=%d requested_tags=%d resolved_keys=%d invalidated=%d recrawled=%d recrawl_errors=%d took=%s",
		job.RequestID,
		job.ActorID,
		workerID,
		len(job.Paths),
		len(job.Tags),
		len(resolved),
		invalidated,
		recrawled,
		recrawlErrs,
		time.Since(start).Truncate(time.Millisecond),
	)
}

func (c *Controller) resolveKeysByTags(tags map[string]struct{}) []string {
	if len(tags) == 0 {
		return nil
	}
	keys := make(map[string]struct{})
	for _, key := range c.rt.CachedKeys() {
		for _, tag := range c.rt.KeyTags(key) {
			if _, ok := tags[tag]; ok {
				keys[key] = struct{}{}
				break
			}
		}
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func randomString(n int) string {
	if n <= 0 {
		return ""
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const max = byte(62 * 4)
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			break
		}
		for _, b := range buf {
			if len(out) >= n {
				break
			}
			if b >= max {
				continue
			}
			out = append(out, alphabet[int(b)%len(alphabet)])
		}
	}
	if len(out) == 0 {
		return "x"
	}
	return string(out)
}
