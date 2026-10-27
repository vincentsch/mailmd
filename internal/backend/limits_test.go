package backend

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

func TestFetchLimitDefaultsAndValidation(t *testing.T) {
	got, err := (FetchRequest{}).WithDefaults()
	if err != nil || got.Limit != 25 || got.MaxMessageBytes != 67108864 || got.MaxBatchBytes != 134217728 {
		t.Fatalf("defaults = %#v, %v", got, err)
	}
	for _, req := range []FetchRequest{{Limit: -1}, {MaxMessageBytes: -1}, {MaxBatchBytes: -1}, {MaxMessageBytes: 2, MaxBatchBytes: 1}, {MaxMessageBytes: math.MaxInt64}, {MaxBatchBytes: math.MaxInt64}} {
		if _, err := req.WithDefaults(); !errors.Is(err, ErrInvalidLimits) {
			t.Fatalf("accepted invalid limits %#v", req)
		}
	}
}

func TestBoundedReaderExactFitAndFirstExcessByte(t *testing.T) {
	for _, size := range []int{0, 10, 11, 100} {
		reader := bytes.NewReader(bytes.Repeat([]byte{'x'}, size))
		data, err := ReadBounded(reader, 10)
		if size <= 10 && (err != nil || len(data) != size) {
			t.Fatalf("fit %d: len=%d err=%v", size, len(data), err)
		}
		if size > 10 && (!errors.Is(err, ErrResponseTooLarge) || data != nil) {
			t.Fatalf("overflow %d: %v", size, err)
		}
		if size-reader.Len() > 11 {
			t.Fatal("read beyond sentinel byte")
		}
	}
}
