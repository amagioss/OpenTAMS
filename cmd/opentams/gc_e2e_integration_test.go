//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// Test plan section 8: the real API wiring of `serve` in development
// auth mode, the real metastore, MinIO, and runGC --once.

type e2eEnv struct {
	*gcEnv
	api *httptest.Server
}

func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()
	e := newGCEnv(t)
	log := zap.NewNop()
	reg, err := newMetricsRegistry(log)
	require.NoError(t, err)
	pool, err := openDB(context.Background(), e.cfg, log)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	obj, err := openObjectStore(e.cfg, log)
	require.NoError(t, err)
	srv, _, err := newAPIServer(e.cfg, log, reg, pool, obj)
	require.NoError(t, err)
	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)
	return &e2eEnv{gcEnv: e, api: api}
}

func (x *e2eEnv) call(t *testing.T, method, path string, body any) (status int, respBody []byte) {
	t.Helper()
	return x.callWith(t, method, path, body, nil)
}

func (x *e2eEnv) callWith(t *testing.T, method, path string, body any, header http.Header) (status int, respBody []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, x.api.URL+path, r)
	require.NoError(t, err)
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer dev")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	respBody, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, respBody
}

func (x *e2eEnv) putFlow(t *testing.T) uuid.UUID {
	t.Helper()
	flowID := uuid.New()
	code, body := x.call(t, http.MethodPut, "/tams/v1/flows/"+flowID.String(), map[string]any{
		"id":        flowID,
		"source_id": uuid.New(),
		"format":    "urn:x-nmos:format:video",
		"codec":     "video/h264",
		"essence_parameters": map[string]any{
			"frame_width": 1920, "frame_height": 1080,
			"frame_rate": map[string]any{"numerator": 25, "denominator": 1},
		},
	})
	require.Equal(t, http.StatusCreated, code, "PUT flow: %s", body)
	return flowID
}

// allocateAndUpload does POST …/storage {"limit": 1} and PUTs 16 bytes to
// the returned URL. It returns the object ID.
func (x *e2eEnv) allocateAndUpload(t *testing.T, flowID uuid.UUID) string {
	t.Helper()
	code, body := x.call(t, http.MethodPost, "/tams/v1/flows/"+flowID.String()+"/storage", map[string]any{"limit": 1})
	require.Equal(t, http.StatusCreated, code, "POST storage: %s", body)
	var alloc struct {
		MediaObjects []struct {
			ObjectID string `json:"object_id"`
			PutURL   struct {
				URL         string  `json:"url"`
				ContentType *string `json:"content-type"`
			} `json:"put_url"`
		} `json:"media_objects"`
	}
	require.NoError(t, json.Unmarshal(body, &alloc))
	require.Len(t, alloc.MediaObjects, 1)
	mo := alloc.MediaObjects[0]

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, mo.PutURL.URL, bytes.NewReader([]byte("0123456789abcdef")))
	require.NoError(t, err)
	if mo.PutURL.ContentType != nil {
		req.Header.Set("Content-Type", *mo.PutURL.ContentType)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "PUT to the upload URL")
	require.True(t, x.keyExists(t, mo.ObjectID), "key after upload")
	return mo.ObjectID
}

func (x *e2eEnv) register(t *testing.T, flowID uuid.UUID, seg map[string]any) int {
	t.Helper()
	code, body := x.callWith(t, http.MethodPost, "/tams/v1/flows/"+flowID.String()+"/segments",
		[]map[string]any{seg}, http.Header{"X-Idempotency-Key": {uuid.NewString()}})
	if code != http.StatusCreated {
		t.Logf("POST segments %d: %s", code, body)
	}
	return code
}

func (x *e2eEnv) deleteFlow(t *testing.T, flowID uuid.UUID) {
	t.Helper()
	code, body := x.call(t, http.MethodDelete, "/tams/v1/flows/"+flowID.String(), nil)
	require.Equal(t, http.StatusNoContent, code, "DELETE flow: %s", body)
}

func (x *e2eEnv) gcOnce(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	log, logs := observedLogger()
	require.Equal(t, 0, runGC(context.Background(), x.cfg, log, true, make(chan struct{})), "gc --once exit code")
	return logs
}

func (x *e2eEnv) refCount(t *testing.T, id string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, x.pool.QueryRow(context.Background(), `SELECT ref_count FROM objects WHERE id = $1`, id).Scan(&n))
	return n
}

func controlledSeg(objectID, tr string) map[string]any {
	return map[string]any{"object_id": objectID, "timerange": tr}
}

// TC-GC-140: the full lifecycle of one object (AC-GC-09).
func Test_TC_GC_140_FullLifecycleOfOneObject(t *testing.T) {
	x := newE2EEnv(t)
	flowF := x.putFlow(t)
	o := x.allocateAndUpload(t, flowF)

	require.Equal(t, http.StatusCreated, x.register(t, flowF, controlledSeg(o, "[0:0_2:0)")))
	assert.False(t, x.rowExists(t, "allocations", o), "allocation row after registration")
	assert.Equal(t, int64(1), x.refCount(t, o))

	x.deleteFlow(t, flowF)
	logs := x.gcOnce(t)

	assert.False(t, x.rowExists(t, "objects", o), "object row after gc")
	assert.False(t, x.keyExists(t, o), "key after gc")
	removed := logs.FilterMessage("gc object removed").FilterField(zap.String("object_id", o)).All()
	require.Len(t, removed, 1)
	assert.Equal(t, "deleted", removed[0].ContextMap()["outcome"])
}

// TC-GC-141: an upload with no registration (BR-GC-40 to BR-GC-44).
func Test_TC_GC_141_UnregisteredUploadIsDeletedAfterExpiry(t *testing.T) {
	x := newE2EEnv(t)
	flowF := x.putFlow(t)
	o := x.allocateAndUpload(t, flowF)
	x.exec(t, `UPDATE allocations SET expires_at = now() - interval '1 hour' WHERE id = $1`, o)

	x.gcOnce(t)

	assert.False(t, x.rowExists(t, "allocations", o), "allocation row after gc")
	assert.False(t, x.keyExists(t, o), "key after gc")
}

// TC-GC-142: a BYOS registration of an allocated ID keeps the bytes
// (BR-GC-14, BR-GC-42).
func Test_TC_GC_142_BYOSRegistrationOfAnAllocatedIDKeepsTheBytes(t *testing.T) {
	x := newE2EEnv(t)
	flowF := x.putFlow(t)
	o := x.allocateAndUpload(t, flowF)

	seg := controlledSeg(o, "[0:0_2:0)")
	seg["get_urls"] = []map[string]any{{"url": "https://cdn.example/x", "label": "cdn"}}
	require.Equal(t, http.StatusCreated, x.register(t, flowF, seg))
	var storageID *string
	require.NoError(t, x.pool.QueryRow(context.Background(), `SELECT storage_id FROM objects WHERE id = $1`, o).Scan(&storageID))
	assert.Nil(t, storageID, "BYOS object row has storage_id NULL")

	x.exec(t, `UPDATE allocations SET expires_at = now() - interval '1 hour' WHERE id = $1`, o)
	x.gcOnce(t)

	assert.False(t, x.rowExists(t, "allocations", o), "allocation row after gc")
	assert.True(t, x.keyExists(t, o), "key after gc")
	assert.Equal(t, int64(1), x.refCount(t, o))
	require.NoError(t, x.pool.QueryRow(context.Background(), `SELECT storage_id FROM objects WHERE id = $1`, o).Scan(&storageID))
	assert.Nil(t, storageID, "object row unchanged")
}

// TC-GC-143: a shared object stays until its last reference goes
// (BR-GC-30, NFR-GC-R1).
func Test_TC_GC_143_SharedObjectStaysUntilTheLastReferenceGoes(t *testing.T) {
	x := newE2EEnv(t)
	flowF, flowG := x.putFlow(t), x.putFlow(t)
	o := x.allocateAndUpload(t, flowF)
	require.Equal(t, http.StatusCreated, x.register(t, flowF, controlledSeg(o, "[0:0_2:0)")))
	require.Equal(t, http.StatusCreated, x.register(t, flowG, controlledSeg(o, "[0:0_2:0)")))

	x.deleteFlow(t, flowF)
	x.gcOnce(t)
	assert.True(t, x.keyExists(t, o), "key after the first gc")
	assert.Equal(t, int64(1), x.refCount(t, o))

	x.deleteFlow(t, flowG)
	x.gcOnce(t)
	assert.False(t, x.keyExists(t, o), "key after the second gc")
	assert.False(t, x.rowExists(t, "objects", o), "object row after the second gc")
}

// TC-GC-144: a comeback before the cycle keeps the object (BR-GC-12 row 1).
func Test_TC_GC_144_ComebackBeforeTheCycleKeepsTheObject(t *testing.T) {
	x := newE2EEnv(t)
	flowF := x.putFlow(t)
	o := x.allocateAndUpload(t, flowF)
	require.Equal(t, http.StatusCreated, x.register(t, flowF, controlledSeg(o, "[0:0_2:0)")))

	code, body := x.call(t, http.MethodDelete,
		"/tams/v1/flows/"+flowF.String()+"/segments?timerange="+url.QueryEscape("[0:0_2:0)"), nil)
	require.Equal(t, http.StatusNoContent, code, "DELETE segments: %s", body)
	require.Equal(t, int64(0), x.refCount(t, o), "released after the segment delete")

	assert.Equal(t, http.StatusCreated, x.register(t, flowF, controlledSeg(o, "[0:0_2:0)")), "second registration")
	x.gcOnce(t)

	assert.True(t, x.keyExists(t, o), "key after gc")
	assert.Equal(t, int64(1), x.refCount(t, o))
}
