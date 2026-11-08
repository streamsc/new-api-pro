package service

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const diagnosticObservedKey = "affinity_diagnostic_observed"
const diagnosticBodyKey = "affinity_diagnostic_body"

var diagnosticContextKeys = []string{"id", "token_id", "user_group", "group", "token_group", "original_model", "resolved_task_model", "task_plugin_key", "platform", "task_action"}

type DiagnosticContextValue struct {
	Kind   string `json:"kind"`
	Int    int    `json:"int,omitempty"`
	String string `json:"string,omitempty"`
}
type DiagnosticMetadata struct {
	CapturedAt         int64                             `json:"captured_at"`
	RequestID          string                            `json:"request_id"`
	UserID             int                               `json:"user_id"`
	Node               string                            `json:"node"`
	Path               string                            `json:"path"`
	Model              string                            `json:"model"`
	Host               string                            `json:"host"`
	UserAgent          string                            `json:"user_agent"`
	BodyKind           string                            `json:"body_kind"`
	BodyStatus         string                            `json:"body_status"`
	AffinityBypass     string                            `json:"affinity_bypass"`
	Headers            http.Header                       `json:"headers"`
	HeadersIncomplete  bool                              `json:"headers_incomplete"`
	HeaderStates       map[string]string                 `json:"header_states"`
	Context            map[string]DiagnosticContextValue `json:"context"`
	MetadataIncomplete bool                              `json:"metadata_incomplete"`
}

func diagnosticSecretHeader(key string, extra []string) bool {
	key = strings.ToLower(key)
	switch key {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "apikey", "x-goog-api-key", "mj-api-secret", "x-auth-token", "x-access-token", "sec-websocket-protocol":
		return true
	}
	for _, excluded := range extra {
		if strings.EqualFold(key, excluded) {
			return true
		}
	}
	return false
}

func diagnosticMetadata(c *gin.Context, task model.AffinityDiagnosticTask, modelName, kind, bypass string) DiagnosticMetadata {
	m := DiagnosticMetadata{CapturedAt: time.Now().UnixMilli(), UserID: task.UserID, BodyKind: kind, AffinityBypass: bypass, Headers: make(http.Header), HeaderStates: map[string]string{}, Context: map[string]DiagnosticContextValue{}}
	var extra []string
	_ = common.UnmarshalJsonStr(task.ExtraExcludedHeaders, &extra)
	userAgent := ""
	if !diagnosticSecretHeader("User-Agent", extra) {
		userAgent = c.Request.UserAgent()
	}
	host := ""
	if !diagnosticSecretHeader("Host", extra) {
		host = c.Request.Host
	}
	// Skip oversized fields whole; never retain a slice of an arbitrarily large value.
	fields := []struct {
		target *string
		value  string
	}{{&m.RequestID, c.GetString(common.RequestIdKey)}, {&m.Node, common.NodeName}, {&m.Path, c.Request.URL.Path}, {&m.Model, modelName}, {&m.Host, host}, {&m.UserAgent, userAgent}}
	for _, field := range fields {
		limit := 4096
		if field.target == &m.RequestID {
			limit = 128
		}
		if len(field.value) > limit || !utf8.ValidString(field.value) {
			m.MetadataIncomplete = true
			continue
		}
		*field.target = strings.Clone(field.value)
	}
	used := 0
	count := 0
	for key, values := range c.Request.Header {
		if count >= 64 {
			m.HeadersIncomplete = true
			break
		}
		count++
		canonical := http.CanonicalHeaderKey(key)
		if len(key) > 128 || !utf8.ValidString(key) {
			m.HeadersIncomplete = true
			continue
		}
		if diagnosticSecretHeader(key, extra) {
			m.HeaderStates[canonical] = "not_collected"
			continue
		}
		size := 0
		valid := true
		for _, v := range values {
			size += len(v)
			valid = valid && utf8.ValidString(v)
		}
		if size > 4096 || !valid || len(values) > 64 {
			m.HeaderStates[canonical] = "over_limit"
			continue
		}
		encoded, err := common.Marshal(map[string][]string{canonical: values})
		if err != nil || used+len(encoded) > 32<<10 {
			m.HeaderStates[canonical] = "over_limit"
			continue
		}
		used += len(encoded)
		copied := make([]string, len(values))
		for i, v := range values {
			copied[i] = strings.Clone(v)
		}
		m.Headers[canonical] = copied
	}
	for _, key := range diagnosticContextKeys {
		value, ok := c.Get(key)
		if !ok {
			continue
		}
		v := DiagnosticContextValue{Kind: "unsupported"}
		switch typed := value.(type) {
		case int:
			v.Kind = "int"
			v.Int = typed
		case string:
			if len(typed) <= 4096 && utf8.ValidString(typed) {
				v.Kind = "string"
				v.String = strings.Clone(typed)
			} else {
				v.Kind = "over_limit"
			}
		}
		m.Context[key] = v
	}
	return m
}

// Observe once at the first routing stage, even when capture is off. This marker
// prevents a task started during a retry from observing the rewritten request.
func CaptureAffinityDiagnostic(c *gin.Context, modelName string, normalized []byte, connectionLocked bool) {
	if _, seen := c.Get(diagnosticObservedKey); seen {
		return
	}
	c.Set(diagnosticObservedKey, true)
	svc := affinityDiagnostics.Load()
	if svc == nil || svc.closed.Load() {
		return
	}
	snapshot := svc.snapshot.Load()
	if snapshot == nil || time.Now().After(snapshot.validUntil) || snapshot.task.UserID != c.GetInt("id") || time.Now().UnixMilli() >= snapshot.task.Deadline {
		return
	}
	task := snapshot.task
	// Reserve both the bounded metadata fields and their encoded queue copy.
	budget := 2 * diagnosticMetadataLimit
	if task.CaptureBody {
		budget += task.MaxBodyBytes
	}
	select {
	case svc.slots <- struct{}{}:
	default:
		svc.drop(task.ID, "queue", false)
		return
	}
	if !svc.memory.TryAcquire(budget) {
		<-svc.slots
		svc.drop(task.ID, "queue", false)
		return
	}
	bypass := ""
	if connectionLocked {
		bypass = "connection_locked"
	} else if _, pinned, _ := GetChannelConstraints(c).ResolvedPin(); pinned {
		bypass = "fixed_channel"
	}
	kind := "http_json"
	if normalized != nil {
		kind = "responses_ws_normalized"
	}
	capture := &diagnosticCapture{svc: svc, task: task, budget: budget, metadata: diagnosticMetadata(c, task, modelName, kind, bypass)}
	if !task.CaptureBody {
		capture.finish(nil, "not_collected")
		return
	}
	if normalized != nil {
		capture.copyBytes(normalized)
		return
	}
	contentType, _, _ := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	if strings.HasSuffix(c.Request.URL.Path, "/realtime") || !(contentType == "application/json" || strings.HasSuffix(contentType, "+json")) {
		capture.finish(nil, "unsupported")
		return
	}
	if raw, ok := c.Get(common.KeyBodyStorage); ok && raw != nil {
		if storage, ok := raw.(common.BodyStorage); ok {
			if storage.Size() > task.MaxBodyBytes {
				capture.finish(nil, "over_limit")
				return
			}
			reader, err := storage.NewReader()
			if err != nil {
				capture.finish(nil, "read_failed")
				return
			}
			defer reader.Close()
			// Allocate the exact bounded size once, independently of the business cursor.
			body := make([]byte, int(storage.Size()))
			_, err = io.ReadFull(reader, body)
			if err != nil {
				capture.finish(nil, "read_failed")
				return
			}
			capture.validate(body)
			return
		}
	}
	if raw, ok := c.Get(common.KeyRequestBody); ok {
		if body, ok := raw.([]byte); ok {
			capture.copyBytes(body)
			return
		}
	}
	if c.Request.ContentLength > task.MaxBodyBytes {
		capture.finish(nil, "over_limit")
		return
	}
	if c.Request.Body == nil {
		capture.finish(nil, "unsupported")
		return
	}
	// No eager read of an unbuffered stream. Only observe bytes the business reads.
	observer := &diagnosticBody{source: c.Request.Body, capture: capture, limit: task.MaxBodyBytes}
	c.Request.Body = observer
	c.Set(diagnosticBodyKey, observer)
}

type diagnosticCapture struct {
	svc      *AffinityDiagnosticService
	task     model.AffinityDiagnosticTask
	budget   int64
	metadata DiagnosticMetadata
	once     sync.Once
}

func (c *diagnosticCapture) copyBytes(body []byte) {
	if int64(len(body)) > c.task.MaxBodyBytes {
		c.finish(nil, "over_limit")
		return
	}
	c.validate(bytes.Clone(body))
}
func (c *diagnosticCapture) validate(body []byte) {
	if !gjson.ValidBytes(body) {
		c.finish(nil, "unsupported")
		return
	}
	c.finish(body, "captured")
}
func (c *diagnosticCapture) finish(body []byte, status string) {
	c.once.Do(func() {
		// A streaming request can outlive the writer; keep no captured fields in
		// its observer after ownership has moved to the bounded queue.
		defer func() { c.metadata = DiagnosticMetadata{} }()
		c.metadata.BodyStatus = status
		metadata, err := common.Marshal(c.metadata)
		if err != nil || int64(len(metadata)) > diagnosticMetadataLimit {
			c.svc.drop(c.task.ID, "queue", false)
			c.svc.memory.Release(c.budget)
			<-c.svc.slots
			return
		}
		size := int64(len(body) + len(metadata))
		if !c.svc.admit(c.task, size) {
			c.svc.memory.Release(c.budget)
			<-c.svc.slots
			return
		}
		sample := &model.AffinityDiagnosticSample{TaskID: c.task.ID, CapturedAt: c.metadata.CapturedAt, RequestID: c.metadata.RequestID, Model: c.metadata.Model, Path: c.metadata.Path, BodyStatus: status, BodyKind: c.metadata.BodyKind, SizeBytes: size, Body: body, Metadata: metadata}
		c.svc.mu.Lock()
		if c.svc.closed.Load() {
			c.svc.mu.Unlock()
			c.svc.drop(c.task.ID, "write", true)
			c.svc.memory.Release(c.budget)
			<-c.svc.slots
			return
		}
		select {
		case c.svc.queue <- diagnosticItem{sample: sample, memory: c.budget}:
			c.svc.mu.Unlock()
		default:
			c.svc.mu.Unlock()
			c.svc.drop(c.task.ID, "queue", false)
			c.svc.memory.Release(c.budget)
			<-c.svc.slots
		}
	})
}

type diagnosticBody struct {
	mu      sync.Mutex
	source  io.ReadCloser
	capture *diagnosticCapture
	limit   int64
	body    []byte
	done    bool
}

func (b *diagnosticBody) Read(p []byte) (int, error) {
	n, err := b.source.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.done {
		if int64(len(b.body)+n) > b.limit {
			b.done = true
			b.body = nil
			b.capture.finish(nil, "over_limit")
		} else {
			if b.body == nil {
				b.body = make([]byte, 0, int(b.limit))
			}
			b.body = append(b.body, p[:n]...)
			if err == io.EOF {
				b.done = true
				b.capture.validate(b.body)
				b.body = nil
			} else if err != nil {
				b.done = true
				b.body = nil
				b.capture.finish(nil, "read_failed")
			}
		}
	}
	return n, err
}
func (b *diagnosticBody) Close() error { err := b.source.Close(); b.Finish(); return err }
func (b *diagnosticBody) Finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.done {
		b.done = true
		b.body = nil
		b.capture.finish(nil, "read_failed")
	}
}
func FinishAffinityDiagnostic(c *gin.Context) {
	if value, ok := c.Get(diagnosticBodyKey); ok {
		if body, ok := value.(*diagnosticBody); ok {
			body.Finish()
		}
	}
}

type DiagnosticSampleInput struct {
	Metadata DiagnosticMetadata
	Body     []byte
}

func (in DiagnosticSampleInput) HeaderValue(key string) (string, string) {
	key = http.CanonicalHeaderKey(key)
	if state, ok := in.Metadata.HeaderStates[key]; ok {
		return "", state
	}
	if _, ok := in.Metadata.Headers[key]; ok {
		return in.Metadata.Headers.Get(key), "present"
	}
	if in.Metadata.HeadersIncomplete {
		return "", "not_collected"
	}
	return "", "missing"
}
func (in DiagnosticSampleInput) ContextValue(key string) (any, string) {
	allowed := false
	for _, candidate := range diagnosticContextKeys {
		if candidate == key {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, "not_collected"
	}
	v, ok := in.Metadata.Context[key]
	if !ok {
		return nil, "missing"
	}
	switch v.Kind {
	case "int":
		return v.Int, "present"
	case "string":
		return v.String, "present"
	case "over_limit":
		return nil, "over_limit"
	default:
		return nil, "type_mismatch"
	}
}
func (in DiagnosticSampleInput) BodyBytes() ([]byte, string) {
	if in.Metadata.BodyStatus != "captured" {
		return nil, in.Metadata.BodyStatus
	}
	return in.Body, "present"
}
