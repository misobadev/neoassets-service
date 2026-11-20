// Package r2test provides an in-memory r2.Client for tests.
package r2test

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"neoassets/pkg/r2"
)

// Memory is a goroutine-safe, in-memory r2.Client. Presigned upload URLs point
// at https://upload.test/<key>; tests put the object with Put to simulate the
// browser upload.
type Memory struct {
	mu      sync.Mutex
	objects map[string][]byte
	types   map[string]string
}

var _ r2.Client = (*Memory)(nil)

// NewMemory returns an empty bucket.
func NewMemory() *Memory {
	return &Memory{objects: map[string][]byte{}, types: map[string]string{}}
}

// Put stores an object, as a browser upload to a presigned URL would.
func (m *Memory) Put(key, contentType string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = append([]byte(nil), data...)
	m.types[key] = contentType
}

// Get returns an object and its content type.
func (m *Memory) Get(key string) ([]byte, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	return data, m.types[key], ok
}

// Keys returns every stored key, sorted.
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.objects))
	for k := range m.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (m *Memory) GenerateSignedUploadURL(_ context.Context, objectName, _ string, _ time.Duration) (string, error) {
	return "https://upload.test/" + objectName, nil
}

func (m *Memory) DeleteObject(_ context.Context, objectName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, objectName)
	delete(m.types, objectName)
	return nil
}

func (m *Memory) UploadFile(_ context.Context, objectName, contentType string, reader io.Reader) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	m.Put(objectName, contentType, data)
	return nil
}

func (m *Memory) UploadFileCached(ctx context.Context, objectName, contentType, _ string, reader io.Reader) error {
	return m.UploadFile(ctx, objectName, contentType, reader)
}

func (m *Memory) CopyObject(_ context.Context, sourceKey, destKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[sourceKey]
	if !ok {
		return fmt.Errorf("NoSuchKey: %s", sourceKey)
	}
	m.objects[destKey] = data
	m.types[destKey] = m.types[sourceKey]
	return nil
}

func (m *Memory) CopyObjectCached(ctx context.Context, sourceKey, destKey, _ string) error {
	return m.CopyObject(ctx, sourceKey, destKey)
}

func (m *Memory) DownloadObject(_ context.Context, objectName string, maxSize int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[objectName]
	if !ok {
		return nil, fmt.Errorf("NoSuchKey: %s", objectName)
	}
	if maxSize > 0 && int64(len(data)) > maxSize {
		return nil, r2.ErrObjectTooLarge
	}
	return append([]byte(nil), data...), nil
}

func (m *Memory) ListKeys(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for _, k := range m.Keys() {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (m *Memory) ListKeysWithSize(_ context.Context, prefix string) ([]r2.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []r2.ObjectInfo
	for k, v := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, r2.ObjectInfo{Key: k, Size: int64(len(v))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *Memory) DeleteObjects(ctx context.Context, keys []string) error {
	for _, k := range keys {
		_ = m.DeleteObject(ctx, k)
	}
	return nil
}

func (m *Memory) ObjectExists(_ context.Context, objectName string) (bool, error) {
	_, _, ok := m.Get(objectName)
	return ok, nil
}

func (m *Memory) ObjectSize(_ context.Context, objectName string) (int64, bool, error) {
	data, _, ok := m.Get(objectName)
	return int64(len(data)), ok, nil
}
