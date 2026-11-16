package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// jsonFields returns the JSON field names of a struct, embedded structs
// flattened.
func jsonFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			out = append(out, jsonFields(f.Type)...)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		out = append(out, name)
	}
	return out
}

// TestEventVocabulary: docs/events.md lists exactly the registered event
// kinds, in the order of ports.EventKinds, and for each emitted kind exactly
// the fields of its data; the kind that is not emitted lists none.
func TestEventVocabulary(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "events.md"))
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("^\\| `(mfgraph\\.[a-z_]+)` \\|(.*)\\|(.*)\\|$")
	field := regexp.MustCompile("`([a-z_]+)`")
	var kinds []string
	docFields := map[string][]string{}
	for _, line := range strings.Split(string(b), "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		kinds = append(kinds, m[1])
		var fs []string
		for _, f := range field.FindAllStringSubmatch(m[3], -1) {
			fs = append(fs, f[1])
		}
		docFields[m[1]] = fs
	}
	if !slices.Equal(kinds, ports.EventKinds) {
		t.Fatalf("docs/events.md lists %v, the code registers %v", kinds, ports.EventKinds)
	}
	if len(eventData) != len(ports.EventKinds) {
		t.Fatalf("eventData has %d kinds", len(eventData))
	}
	var emitted []string
	for _, k := range ports.EventKinds {
		data, ok := eventData[k]
		if !ok {
			t.Fatalf("no data for %s", k)
		}
		var want []string
		if data != nil {
			emitted = append(emitted, k)
			want = jsonFields(reflect.TypeOf(data))
		}
		got := docFields[k]
		sort.Strings(want)
		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: docs/events.md lists %v, the data has %v", k, got, want)
		}
	}
	if !slices.Equal(emitted, ports.EmittedEventKinds) {
		t.Errorf("emitted kinds %v, ports.EmittedEventKinds %v", emitted, ports.EmittedEventKinds)
	}
	for _, k := range ports.EventKinds {
		if len(k) > 64 || !strings.HasPrefix(k, "mfgraph.") {
			t.Errorf("event kind %q: the outbox columns hold 64 characters of mfgraph.<verb>", k)
		}
	}
}

// TestEventSize: an envelope above 256 KiB is refused, never truncated,
// and the largest envelope a commit can produce stays far below 64 KiB.
func TestEventSize(t *testing.T) {
	huge := envelope{V: 1, Data: map[string]string{"x": strings.Repeat("x", MaxEventBytes)}}
	if _, err := marshalEvent(huge); !errors.Is(err, errEventTooLarge) {
		t.Fatalf("a huge envelope: %v", err)
	}
	ref255 := "@" + strings.Repeat("u", 255-len("@:example.com")) + ":example.com"
	typ := "abcdefghijklmnop"
	obo := actorOf(ref255, typ)
	worst := envelope{V: 1, EventID: eventID(ports.EventSnapshotRestored, "01JD3X5Q8Z9W2V4T6R8P0N2P01", "01JD3X5Q8Z9W2V4T6R8P0N2S01", 1),
		OccurredAt: rfc3339(1), ProjectID: "01JD3X5Q8Z9W2V4T6R8P0N2P01", Actor: actorOf(ref255, typ),
		Data: restoreData{commitData: commitData{WorkspaceID: "01JD3X5Q8Z9W2V4T6R8P0N2W01", SnapshotID: "01JD3X5Q8Z9W2V4T6R8P0N2S01",
			BranchID: "01JD3X5Q8Z9W2V4T6R8P0N2B01", RefName: "workstreams/01JD3X5Q8Z9W2V4T6R8P0N2B01/" + strings.Repeat("\u00e9", 127),
			RootTreeHash: "sha256:" + strings.Repeat("a", 64), ParentIDs: []string{"01JD3X5Q8Z9W2V4T6R8P0N2S00", "01JD3X5Q8Z9W2V4T6R8P0N2S02"},
			TriggerKind: "restore", EntryCount: 1 << 40, TotalBytes: 1 << 52, OnBehalfOf: &obo},
			RestoredFrom: "01JD3X5Q8Z9W2V4T6R8P0N2S00"}}
	b, err := marshalEvent(worst)
	if err != nil || len(b) >= 64<<10 {
		t.Fatalf("the largest envelope: %d bytes, %v", len(b), err)
	}
	t.Logf("the largest envelope of a commit or restore has %d bytes", len(b))
}
