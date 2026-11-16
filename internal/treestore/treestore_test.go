package treestore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/LUOSENGWA/MFGraph/internal/blob/posix"
	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
	"github.com/LUOSENGWA/MFGraph/internal/treestore"
)

var ctx = context.Background()

// readAll returns a function so it can be applied to a (ReadCloser, error)
// call directly: readAll(t)(s.Get(...)).
func readAll(t *testing.T) func(io.ReadCloser, error) string {
	return func(rc io.ReadCloser, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

// TestPOSIXRoundTrip stores a tree through the POSIX blob store and walks
// it back (the other tests use the in-memory blob store).
func TestPOSIXRoundTrip(t *testing.T) {
	const ws = "01JD3X5Q8Z9W2V4T6R8P0N2M4K"
	blobs, err := posix.New(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts, err := treestore.New(blobs, treestore.StaticPrefixes(map[string]string{ws: "tenants/T/projects/P/workspaces/" + ws}))
	if err != nil {
		t.Fatal(err)
	}
	var files []tree.FileEntry
	for i := 0; i < 2100; i++ {
		p := fmt.Sprintf("d%d/f%05d", i%3, i)
		files = append(files, tree.FileEntry{Path: p, Hash: contenthash.SumBytes([]byte(p)), Size: int64(len(p)), Mode: tree.ModeRegular})
	}
	root, nodes, err := tree.Build(files)
	if err != nil {
		t.Fatal(err)
	}
	for h, b := range nodes {
		if err := ts.Put(ctx, ws, h, b); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	if err := tree.Walk(ctx, ts.Getter(ws), root, func(string, tree.Entry) error { n++; return nil }); err != nil || n != 2103 {
		t.Fatalf("walk through the POSIX store: %d entries, %v", n, err)
	}
}

func TestCachedPrefixes(t *testing.T) {
	calls := 0
	r := treestore.CachedPrefixes(func(_ context.Context, ws string) (string, error) {
		calls++
		if ws == "missing" {
			return "", ports.ErrNotFound
		}
		return "p/" + ws, nil
	}, 2)
	for _, ws := range []string{"a", "a", "b", "a", "c", "a"} {
		if p, err := r(ctx, ws); err != nil || p != "p/"+ws {
			t.Fatalf("%s: %q %v", ws, p, err)
		}
	}
	if _, err := r(ctx, "missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := r(ctx, "missing"); !errors.Is(err, ports.ErrNotFound) || calls != 6 {
		t.Fatalf("errors must not be cached; %d calls", calls)
	}
}

func TestTreeStoreRoundTrip(t *testing.T) {
	const ws = "01JD3X5Q8Z9W2V4T6R8P0N2M4K"
	prefix := "tenants/T/projects/P/workspaces/" + ws
	ts, blobs := memory.NewTreeStore(treestore.StaticPrefixes(map[string]string{ws: prefix}))
	files := []tree.FileEntry{
		{Path: "docs/a.txt", Hash: contenthash.SumBytes([]byte("a")), Size: 1, Mode: tree.ModeRegular},
		{Path: "bin/tool", Hash: contenthash.SumBytes([]byte("t")), Size: 1, Mode: tree.ModeExecutable},
	}
	root, nodes, err := tree.Build(files)
	if err != nil {
		t.Fatal(err)
	}
	for h, b := range nodes {
		if err := ts.Put(ctx, ws, h, b); err != nil {
			t.Fatal(err)
		}
		if err := ts.Put(ctx, ws, h, b); err != nil { // idempotent
			t.Fatal(err)
		}
	}
	// Stored under tree/<hh>/<hex>.json.zst below the workspace prefix, compressed.
	for h, canonical := range nodes {
		key := storagekey.Join(prefix, storagekey.TreeKey(h))
		z := readAll(t)(blobs.Get(ctx, key, 0, -1))
		dec, _ := zstd.NewReader(nil)
		raw, err := dec.DecodeAll([]byte(z), nil)
		if err != nil || !bytes.Equal(raw, canonical) {
			t.Fatalf("stored object %s is not the zstd of the canonical bytes: %v", key, err)
		}
	}
	// Walking through the store restores the files.
	n := 0
	if err := tree.Walk(ctx, ts.Getter(ws), root, func(p string, e tree.Entry) error {
		if e.Type == tree.TypeFile {
			n++
		}
		return nil
	}); err != nil || n != 2 {
		t.Fatalf("walk: %d files, %v", n, err)
	}
	// Wrong hash is refused; unknown workspace and unknown node are not found.
	if err := ts.Put(ctx, ws, contenthash.SumBytes([]byte("x")), nodes[root]); !errors.Is(err, ports.ErrHashMismatch) {
		t.Fatalf("Put with wrong hash: %v", err)
	}
	if _, err := ts.Get(ctx, "01JD3X5Q8Z9W2V4T6R8P0N2M4A", root); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown workspace: %v", err)
	}
	if _, err := ts.Get(ctx, ws, contenthash.SumBytes([]byte("nope"))); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown node: %v", err)
	}
}

// TestTreeStoreRoundTripsLargeNodes: nodes of every size up to the 1 MiB
// limit are read back (the decoder once had a 4096-byte output limit, so a
// directory of 31 entries, 4160 bytes, could be stored but not read).
func TestTreeStoreRoundTripsLargeNodes(t *testing.T) {
	const ws = "W"
	ts, _ := memory.NewTreeStore(treestore.StaticPrefixes(map[string]string{ws: "p"}))
	files := func(n int, name func(int) string) []tree.FileEntry {
		var out []tree.FileEntry
		for i := 0; i < n; i++ {
			p := name(i)
			out = append(out, tree.FileEntry{Path: p, Hash: contenthash.SumBytes([]byte(p)), Size: int64(len(p)), Mode: tree.ModeRegular})
		}
		return out
	}
	roundTrip := func(label string, nodes map[contenthash.Hash][]byte) {
		t.Helper()
		for h, b := range nodes {
			if err := ts.Put(ctx, ws, h, b); err != nil {
				t.Fatalf("%s: Put: %v", label, err)
			}
		}
		for h, b := range nodes {
			got, err := ts.Get(ctx, ws, h)
			if err != nil || !bytes.Equal(got, b) {
				t.Fatalf("%s: Get %d-byte node: %v", label, len(b), err)
			}
		}
	}
	build := func(label string, fs []tree.FileEntry, minNodeBytes int) {
		t.Helper()
		root, nodes, err := tree.Build(fs)
		if err != nil {
			t.Fatal(err)
		}
		largest := 0
		for _, b := range nodes {
			largest = max(largest, len(b))
		}
		if largest < minNodeBytes {
			t.Fatalf("%s: largest node has %d bytes, want at least %d", label, largest, minNodeBytes)
		}
		roundTrip(label, nodes)
		n := 0
		if err := tree.Walk(ctx, ts.Getter(ws), root, func(string, tree.Entry) error { n++; return nil }); err != nil || n != len(fs) {
			t.Fatalf("%s: walk through the store: %d entries, %v", label, n, err)
		}
	}
	build("31 entries", files(31, func(i int) string { return fmt.Sprintf("file%02d", i) }), 4097)
	build("1025 entries (paged)", files(1025, func(i int) string { return fmt.Sprintf("f%04d", i) }), 100_000)
	// The largest node valid names produce: 1024 entries with 255-byte names
	// that consist of '"' (each escaped to two bytes) and a number.
	build("largest valid node", files(1024, func(i int) string { return strings.Repeat(`"`, 251) + fmt.Sprintf("%04d", i) }), 640_000)

	// The store treats nodes as opaque bytes: exactly tree.MaxNodeBytes of
	// incompressible data round-trip, one byte more is refused.
	big := make([]byte, tree.MaxNodeBytes)
	for i := 0; i < len(big); i += 32 {
		sum := sha256.Sum256([]byte(fmt.Sprint(i)))
		copy(big[i:], sum[:])
	}
	roundTrip("1 MiB", map[contenthash.Hash][]byte{tree.HashOf(big): big})
	tooBig := append(big, 'x')
	if err := ts.Put(ctx, ws, tree.HashOf(tooBig), tooBig); err == nil {
		t.Fatal("Put accepted a node above tree.MaxNodeBytes")
	}
}

func TestTreeStoreDetectsCorruptionAndBombs(t *testing.T) {
	const ws = "W"
	blobs := memory.NewBlobStore()
	ts, err := treestore.New(blobs, treestore.StaticPrefixes(map[string]string{ws: "p"}))
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := zstd.NewWriter(nil)
	put := func(h contenthash.Hash, raw []byte) {
		z := enc.EncodeAll(raw, nil)
		if _, err := blobs.PutIfAbsent(ctx, storagekey.Join("p", storagekey.TreeKey(h)), bytes.NewReader(z), int64(len(z)), contenthash.SumBytes(z)); err != nil {
			t.Fatal(err)
		}
	}
	// A stored object whose content does not match its key.
	h := contenthash.SumBytes([]byte(`{"entries":[],"format":1,"kind":"dir"}`))
	put(h, []byte(`{"entries":[],"format":1,"kind":"dir_page"}`))
	if _, err := ts.Get(ctx, ws, h); !errors.Is(err, ports.ErrHashMismatch) {
		t.Fatalf("corrupt node: %v", err)
	}
	// A small object that expands beyond the node limit.
	bomb := []byte(strings.Repeat("a", tree.MaxNodeBytes+1))
	hb := contenthash.SumBytes(bomb)
	put(hb, bomb)
	if _, err := ts.Get(ctx, ws, hb); err == nil {
		t.Fatal("decompression beyond the node limit was accepted")
	}
	if err := ts.Put(ctx, ws, hb, bomb); err == nil {
		t.Fatal("oversized node accepted by Put")
	}
	// The same without a content size in the frame header (streaming encoder).
	var streamed bytes.Buffer
	w, _ := zstd.NewWriter(&streamed)
	if _, err := w.Write(bomb); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	var hdr zstd.Header
	if err := hdr.Decode(streamed.Bytes()); err != nil || hdr.HasFCS {
		t.Fatalf("streamed frame header: %+v, %v", hdr, err)
	}
	hs := contenthash.SumBytes(append([]byte("streamed"), bomb...))
	if _, err := blobs.PutIfAbsent(ctx, storagekey.Join("p", storagekey.TreeKey(hs)), bytes.NewReader(streamed.Bytes()), int64(streamed.Len()), contenthash.SumBytes(streamed.Bytes())); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Get(ctx, ws, hs); err == nil {
		t.Fatal("streamed decompression beyond the node limit was accepted")
	}
	// A streamed frame within the limit decodes.
	ok := tree.Encode(tree.Node{Kind: tree.KindDir, Entries: []tree.Entry{{Name: "s", Type: tree.TypeFile, Hash: contenthash.SumBytes(nil), Mode: tree.ModeRegular}}})
	streamed.Reset()
	w.Reset(&streamed)
	if _, err := w.Write(ok); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	ho := contenthash.SumBytes(ok)
	if _, err := blobs.PutIfAbsent(ctx, storagekey.Join("p", storagekey.TreeKey(ho)), bytes.NewReader(streamed.Bytes()), int64(streamed.Len()), contenthash.SumBytes(streamed.Bytes())); err != nil {
		t.Fatal(err)
	}
	if got, err := ts.Get(ctx, ws, ho); err != nil || !bytes.Equal(got, ok) {
		t.Fatalf("streamed frame: %q, %v", got, err)
	}
}

// TestTreeStoreFrameLimits: the zstd window and the output are limited
// independently (content format 4.1). Frames of a streaming compressor that
// declare an 8 MiB window and no content size read back for nodes from the
// 38-byte empty root to the 1 MiB limit; a window above 8 MiB, output above
// tree.MaxNodeBytes, a dictionary and a hash mismatch are rejected. Put
// writes one frame with Frame_Content_Size and no dictionary.
func TestTreeStoreFrameLimits(t *testing.T) {
	const ws = "W"
	// read stores z as the object of node h in a new store and reads it back.
	read := func(h contenthash.Hash, z []byte) ([]byte, error) {
		t.Helper()
		blobs := memory.NewBlobStore()
		ts, err := treestore.New(blobs, treestore.StaticPrefixes(map[string]string{ws: "p"}))
		if err != nil {
			t.Fatal(err)
		}
		key := storagekey.Join("p", storagekey.TreeKey(h))
		if _, err := blobs.PutIfAbsent(ctx, key, bytes.NewReader(z), int64(len(z)), contenthash.SumBytes(z)); err != nil {
			t.Fatal(err)
		}
		return ts.Get(ctx, ws, h)
	}
	// stream compresses raw like a streaming compressor that does not know
	// the input size: one frame that declares the window and no content size.
	stream := func(raw []byte, window int, opts ...zstd.EOption) []byte {
		t.Helper()
		var buf bytes.Buffer
		w, err := zstd.NewWriter(&buf, append([]zstd.EOption{zstd.WithWindowSize(window), zstd.WithEncoderConcurrency(1)}, opts...)...)
		if err != nil {
			t.Fatal(err)
		}
		// Flush writes the frame header before the end of the input is
		// known; on Close alone the encoder compresses a small input in one
		// shot and picks a window that just fits it.
		if _, err := w.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		var hdr zstd.Header
		if err := hdr.Decode(buf.Bytes()); err != nil || hdr.HasFCS || hdr.SingleSegment || hdr.WindowSize != uint64(window) {
			t.Fatalf("streamed frame header: %+v, %v", hdr, err)
		}
		return buf.Bytes()
	}
	// dir returns the single node of a directory of n files.
	dir := func(n int, name func(int) string) []byte {
		t.Helper()
		var fs []tree.FileEntry
		for i := 0; i < n; i++ {
			fs = append(fs, tree.FileEntry{Path: name(i), Hash: contenthash.SumBytes([]byte(name(i))), Size: 1, Mode: tree.ModeRegular})
		}
		root, nodes, err := tree.Build(fs)
		if err != nil || len(nodes) != 1 {
			t.Fatalf("%d files: %d nodes, %v", n, len(nodes), err)
		}
		return nodes[root]
	}
	emptyRoot := []byte(`{"entries":[],"format":1,"kind":"dir"}`)
	limit := make([]byte, tree.MaxNodeBytes) // opaque and incompressible
	for i := 0; i < len(limit); i += 32 {
		sum := sha256.Sum256([]byte(fmt.Sprint(i)))
		copy(limit[i:], sum[:])
	}
	nodes := []struct {
		name string
		raw  []byte
	}{
		{"empty root", emptyRoot},
		{"1024 entries", dir(1024, func(i int) string { return fmt.Sprintf("f%04d", i) })},
		// 1024 entries with 255-byte names of '"' (escaped to two bytes).
		{"largest valid node", dir(1024, func(i int) string { return strings.Repeat(`"`, 251) + fmt.Sprintf("%04d", i) })},
		{"1 MiB", limit},
	}
	if len(emptyRoot) != 38 || len(nodes[2].raw) < 640_000 {
		t.Fatalf("test nodes have %d and %d bytes", len(emptyRoot), len(nodes[2].raw))
	}
	for _, n := range nodes {
		h := tree.HashOf(n.raw)
		// Accepted: an 8 MiB window and no content size, whatever the size.
		if got, err := read(h, stream(n.raw, treestore.MaxFrameWindow)); err != nil || !bytes.Equal(got, n.raw) {
			t.Errorf("%s (%d bytes) in a frame with an 8 MiB window: %v", n.name, len(n.raw), err)
		}
		// Put writes one frame with Frame_Content_Size and no dictionary.
		ts, blobs := memory.NewTreeStore(treestore.StaticPrefixes(map[string]string{ws: "p"}))
		if err := ts.Put(ctx, ws, h, n.raw); err != nil {
			t.Fatal(err)
		}
		z := readAll(t)(blobs.Get(ctx, storagekey.Join("p", storagekey.TreeKey(h)), 0, -1))
		var hdr zstd.Header
		if err := hdr.Decode([]byte(z)); err != nil || !hdr.HasFCS || hdr.FrameContentSize != uint64(len(n.raw)) || hdr.DictionaryID != 0 || hdr.Skippable {
			t.Errorf("%s: Put wrote the frame header %+v, %v", n.name, hdr, err)
		}
	}

	h := tree.HashOf(emptyRoot)
	// Rejected: a window above 8 MiB, even for the 38-byte empty root.
	if _, err := read(h, stream(emptyRoot, 2*treestore.MaxFrameWindow)); !errors.Is(err, zstd.ErrWindowSizeExceeded) {
		t.Errorf("16 MiB window: %v", err)
	}
	// Rejected: output above the node limit, from a frame with an 8 MiB
	// window and no content size. Decoding stops at the limit: reading a
	// frame of a few kilobytes that expands to 64 MiB allocates a few MiB
	// (3.5 MiB measured; 16.7 MiB if only the 8 MiB memory limit held).
	for _, size := range []int{tree.MaxNodeBytes + 1, 64 << 20} {
		bomb := make([]byte, size)
		hb, z := tree.HashOf(bomb), stream(bomb, treestore.MaxFrameWindow)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := read(hb, z)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, zstd.ErrDecoderSizeExceeded) {
			t.Errorf("%d bytes from a %d-byte frame: %v", size, len(z), err)
		}
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8<<20 {
			t.Errorf("%d bytes from a %d-byte frame: reading allocated %d bytes", size, len(z), alloc)
		}
	}
	// Rejected: content that does not match the hash in the key.
	if _, err := read(h, stream(dir(1, func(int) string { return "x" }), treestore.MaxFrameWindow)); !errors.Is(err, ports.ErrHashMismatch) {
		t.Errorf("hash mismatch: %v", err)
	}
	// Rejected: a frame that needs a dictionary.
	if _, err := read(h, stream(emptyRoot, treestore.MaxFrameWindow, zstd.WithEncoderDictRaw(7, []byte(`{"entries":[`)))); !errors.Is(err, zstd.ErrUnknownDictionary) {
		t.Errorf("dictionary: %v", err)
	}
}
