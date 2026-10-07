package r2

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// failingReader fails the test if anything reads from it.
type failingReader struct{ t *testing.T }

func (f failingReader) Read([]byte) (int, error) {
	f.t.Fatal("body was read although its reported size is over the limit")
	return 0, io.EOF
}

func TestReadBoundedRefusesOversizedObjectWithoutReading(t *testing.T) {
	size := int64(11)
	if _, err := readBounded(failingReader{t}, &size, 10); !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("got %v, want ErrObjectTooLarge", err)
	}
}

// endlessReader serves bytes forever and fails the test once more than limit
// bytes have been read from it.
type endlessReader struct {
	t     *testing.T
	read  int
	limit int
}

func (e *endlessReader) Read(p []byte) (int, error) {
	e.read += len(p)
	if e.read > e.limit {
		e.t.Fatalf("read %d bytes past a 10-byte limit", e.read)
	}
	return len(p), nil
}

// The reported length can be missing or not match what is streamed (an object
// replaced between the size check and the download), so the read itself stops
// at the limit instead of pulling the whole body.
func TestReadBoundedStopsAtTheLimit(t *testing.T) {
	small := int64(4)
	for _, length := range []*int64{&small, nil} {
		body := &endlessReader{t: t, limit: 64 << 10}
		if _, err := readBounded(body, length, 10); !errors.Is(err, ErrObjectTooLarge) {
			t.Fatalf("got %v, want ErrObjectTooLarge", err)
		}
	}
}

func TestReadBoundedReturnsObjectsUpToTheLimit(t *testing.T) {
	for _, n := range []int{0, 1, 10} {
		data := bytes.Repeat([]byte("x"), n)
		size := int64(n)
		understated := int64(n / 2)
		for _, length := range []*int64{&size, nil, &understated} {
			got, err := readBounded(bytes.NewReader(data), length, 10)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%d bytes: got %d bytes, err %v", n, len(got), err)
			}
		}
	}
}

// A correctly reported length sizes the buffer once; reading to the end must
// not double it.
func TestReadBoundedAllocatesOnce(t *testing.T) {
	n := 10 << 20
	size := int64(n)
	got, err := readBounded(bytes.NewReader(make([]byte, n)), &size, int64(n))
	if err != nil {
		t.Fatal(err)
	}
	// Allocation rounding adds a little; doubling would add n.
	if cap(got) > n+n/8 {
		t.Fatalf("buffer grew to %d bytes for a %d-byte object", cap(got), n)
	}
}
