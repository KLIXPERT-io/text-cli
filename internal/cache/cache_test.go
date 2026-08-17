package cache

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPutGetRoundTrip(t *testing.T) {
	s := New(t.TempDir(), time.Hour)
	key := Key("readability", []string{"--lang=de"}, "", "")
	payload := json.RawMessage(`{"flesch":42}`)

	if err := s.Put(key, payload, 0); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil entry after Put, want a hit")
	}
	if string(got.Payload) != string(payload) {
		t.Errorf("Payload = %s, want %s", got.Payload, payload)
	}
}

func TestGetMissingKeyIsNilNil(t *testing.T) {
	s := New(t.TempDir(), time.Hour)

	got, err := s.Get("does-not-exist")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get on missing key = %+v, want nil", got)
	}
}

func TestGetExpiredEntryIsMiss(t *testing.T) {
	s := New(t.TempDir(), time.Hour)
	key := Key("entities", nil, "prop", "user@example.com")
	payload := json.RawMessage(`{"a":1}`)

	// Put treats a TTL <= 0 as "use the store default" rather than "already
	// expired" (see Put), so a past-TTL entry is produced with a tiny positive
	// TTL that has elapsed by the time Get runs. A subsequent Get must report
	// a miss (and clean the file up) rather than returning stale data.
	if err := s.Put(key, payload, time.Nanosecond); err != nil {
		t.Fatalf("Put: %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	got, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get on expired entry = %+v, want nil (miss)", got)
	}
}

func TestInvalidate(t *testing.T) {
	s := New(t.TempDir(), time.Hour)
	key := Key("metrics", []string{"a", "b"}, "", "")
	payload := json.RawMessage(`{"ok":true}`)

	if err := s.Put(key, payload, 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Invalidate(key); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	got, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get after Invalidate: %v", err)
	}
	if got != nil {
		t.Errorf("Get after Invalidate = %+v, want nil", got)
	}

	// Invalidating an already-missing key must not error.
	if err := s.Invalidate(key); err != nil {
		t.Errorf("Invalidate on missing key: %v", err)
	}
}

func TestKeyStableAndOrderInsensitive(t *testing.T) {
	a := Key("readability", []string{"--lang=de", "--file=x"}, "prop", "id")
	b := Key("readability", []string{"--lang=de", "--file=x"}, "prop", "id")
	if a != b {
		t.Errorf("Key is not stable across identical calls: %s != %s", a, b)
	}

	// Reordering the args slice must not change the key: callers assemble args
	// from flags in whatever order the user supplied them, and that order is
	// not semantically meaningful for cache identity.
	c := Key("readability", []string{"--file=x", "--lang=de"}, "prop", "id")
	if a != c {
		t.Errorf("Key is order-sensitive for args: %s != %s", a, c)
	}

	// Sanity: a different property or identity must still change the key.
	d := Key("readability", []string{"--lang=de", "--file=x"}, "other-prop", "id")
	if a == d {
		t.Error("Key did not change when property changed")
	}
}

func TestKeyDoesNotMutateInputSlice(t *testing.T) {
	args := []string{"z", "a", "m"}
	orig := append([]string(nil), args...)
	_ = Key("cmd", args, "", "")
	for i := range args {
		if args[i] != orig[i] {
			t.Fatalf("Key mutated caller's args slice: got %v, want %v", args, orig)
		}
	}
}
