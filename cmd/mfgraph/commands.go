package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

// The client commands are a thin layer over the REST API: every rule
// (paths, modes, conflicts, permissions) is the server's, and its error
// responses are printed as received.

// args separates the flags of a command from its positional arguments.
// Flags may appear anywhere; a flag of repeated may be given several
// times. "--" ends the flags.
type args struct {
	pos      []string
	flags    map[string][]string
	switches map[string]bool
}

func parseArgs(in []string, valued, repeated []string, switches []string) (args, error) {
	a := args{flags: map[string][]string{}, switches: map[string]bool{}}
	isValued := map[string]bool{}
	for _, f := range append(valued, repeated...) {
		isValued[f] = true
	}
	isRepeated := map[string]bool{}
	for _, f := range repeated {
		isRepeated[f] = true
	}
	isSwitch := map[string]bool{}
	for _, f := range switches {
		isSwitch[f] = true
	}
	for i := 0; i < len(in); i++ {
		arg := in[i]
		if arg == "--" {
			a.pos = append(a.pos, in[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			a.pos = append(a.pos, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch {
		case isSwitch[name]:
			a.switches[name] = true
		case isValued[name]:
			if !hasValue {
				if i+1 >= len(in) {
					return a, fmt.Errorf("%s needs a value", name)
				}
				i++
				value = in[i]
			}
			if len(a.flags[name]) > 0 && !isRepeated[name] {
				return a, fmt.Errorf("%s given twice", name)
			}
			a.flags[name] = append(a.flags[name], value)
		default:
			return a, fmt.Errorf("unknown flag %s", name)
		}
	}
	return a, nil
}

func (a args) flag(names ...string) string {
	for _, n := range names {
		if v := a.flags[n]; len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// cli holds what every command needs.
type cli struct {
	ctx    context.Context
	c      *gen.Client
	stdout io.Writer
	stderr io.Writer
	json   bool // print the response bodies as JSON
}

func (x *cli) printJSON(v any) error {
	enc := json.NewEncoder(x.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// usageError is a mistake in the command line (exit status 2).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

func (x *cli) projectCreate(a args) error {
	if len(a.pos) != 1 {
		return usagef("usage: mfgraph project create <name>")
	}
	var p gen.Project
	if err := decode(call(x.c.CreateProject(x.ctx, &gen.CreateProjectParams{}, gen.CreateProjectRequest{Name: a.pos[0]})),
		x.stderr, &p, http.StatusCreated); err != nil {
		return err
	}
	if x.json {
		return x.printJSON(p)
	}
	fmt.Fprintln(x.stdout, p.ProjectId)
	return nil
}

func (x *cli) workspaceCreate(a args) error {
	if len(a.pos) != 1 {
		return usagef("usage: mfgraph workspace create <project_id> [--name <display name>]")
	}
	req := gen.CreateWorkspaceRequest{ProjectId: a.pos[0]}
	if n := a.flag("--name"); n != "" {
		req.DisplayName = &n
	}
	var w gen.Workspace
	if err := decode(call(x.c.CreateWorkspace(x.ctx, &gen.CreateWorkspaceParams{}, req)), x.stderr, &w, http.StatusCreated); err != nil {
		return err
	}
	if x.json {
		return x.printJSON(w)
	}
	fmt.Fprintf(x.stdout, "%s\tmain_branch=%s\n", w.WorkspaceId, w.MainBranchId)
	return nil
}

// upload stages one local file and returns the object.
func (x *cli) upload(ws, file string) (gen.StagedObject, error) {
	f, err := os.Open(file)
	if err != nil {
		return gen.StagedObject{}, err
	}
	defer f.Close()
	h, _, err := contenthash.Sum(f) // the checksum the server verifies
	if err != nil {
		return gen.StagedObject{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return gen.StagedObject{}, err
	}
	sum := h.String()
	var o gen.StagedObject
	err = decode(call(x.c.PutObjectWithBody(x.ctx, ws, &gen.PutObjectParams{XContentSHA256: &sum}, "application/octet-stream", f)),
		x.stderr, &o, http.StatusOK, http.StatusCreated)
	return o, err
}

func (x *cli) uploadCmd(a args) error {
	if len(a.pos) < 2 {
		return usagef("usage: mfgraph upload <workspace_id> <file>...")
	}
	for _, file := range a.pos[1:] {
		o, err := x.upload(a.pos[0], file)
		if err != nil {
			return err
		}
		if x.json {
			if err := x.printJSON(o); err != nil {
				return err
			}
			continue
		}
		fmt.Fprintf(x.stdout, "%s\t%d\t%s\t%s\n", o.Hash, o.Size, o.Status, file)
	}
	return nil
}

// branch returns a branch of a workspace (the main branch when id is "").
func (x *cli) branch(ws, id string) (gen.Branch, error) {
	if id == "" {
		var w gen.Workspace
		if err := decode(call(x.c.GetWorkspace(x.ctx, ws, &gen.GetWorkspaceParams{})), x.stderr, &w); err != nil {
			return gen.Branch{}, err
		}
		id = w.MainBranchId
	}
	var cursor *string
	for {
		var l gen.BranchList
		if err := decode(call(x.c.ListBranches(x.ctx, ws, &gen.ListBranchesParams{Cursor: cursor})), x.stderr, &l); err != nil {
			return gen.Branch{}, err
		}
		for _, b := range l.Items {
			if b.BranchId == id {
				return b, nil
			}
		}
		if l.NextCursor == nil {
			return gen.Branch{}, fmt.Errorf("branch %s is not a branch of workspace %s", id, ws)
		}
		cursor = l.NextCursor
	}
}

func (x *cli) commit(a args) error {
	if len(a.pos) < 1 || len(a.pos) == 1 && len(a.flags["--delete"]) == 0 {
		return usagef("usage: mfgraph commit <workspace_id> [--branch <id>] [-m <message>] <path>=<file>... [--delete <path>]...")
	}
	ws := a.pos[0]
	var changes []gen.Change
	for _, spec := range a.pos[1:] {
		path, file, ok := strings.Cut(spec, "=")
		if !ok || path == "" || file == "" {
			return usagef("mfgraph commit: %q is not <path>=<file>", spec)
		}
		o, err := x.upload(ws, file)
		if err != nil {
			return err
		}
		h := o.Hash
		changes = append(changes, gen.Change{Path: path, Hash: &h})
	}
	for _, path := range a.flags["--delete"] {
		t := true
		changes = append(changes, gen.Change{Path: path, Delete: &t})
	}
	b, err := x.branch(ws, a.flag("--branch"))
	if err != nil {
		return err
	}
	req := gen.CommitRequest{BranchId: b.BranchId, ExpectedGeneration: b.Generation, Changes: changes}
	if m := a.flag("-m", "--message"); m != "" {
		req.Message = &m
	}
	var r gen.CommitResult
	if err := decode(call(x.c.CreateCommit(x.ctx, ws, &gen.CreateCommitParams{}, req)), x.stderr, &r, http.StatusCreated); err != nil {
		return err
	}
	if x.json {
		return x.printJSON(r)
	}
	fmt.Fprintln(x.stdout, r.SnapshotId)
	return nil
}

func (x *cli) log(a args) error {
	if len(a.pos) != 1 {
		return usagef("usage: mfgraph log <workspace_id> [--branch <id>]")
	}
	params := gen.GetTimelineParams{}
	if b := a.flag("--branch"); b != "" {
		params.BranchId = &b
	}
	for {
		var l gen.SnapshotList
		if err := decode(call(x.c.GetTimeline(x.ctx, a.pos[0], &params)), x.stderr, &l); err != nil {
			return err
		}
		for _, s := range l.Items {
			if x.json {
				if err := x.printJSON(s); err != nil {
					return err
				}
				continue
			}
			fmt.Fprintf(x.stdout, "%s\t%s\t%s\t%s\t%d files\t%s\n", s.SnapshotId, s.CreatedAt.Format("2006-01-02T15:04:05.000000Z"),
				s.TriggerKind, s.AuthorRef, s.EntryCount, s.Message)
		}
		if l.NextCursor == nil {
			return nil
		}
		params.Cursor = l.NextCursor
	}
}

func (x *cli) ls(a args) error {
	if len(a.pos) < 1 || len(a.pos) > 2 {
		return usagef("usage: mfgraph ls <snapshot_id> [path]")
	}
	params := gen.ListSnapshotTreeParams{}
	if len(a.pos) == 2 {
		params.Path = &a.pos[1]
	}
	for {
		var l gen.TreeListing
		if err := decode(call(x.c.ListSnapshotTree(x.ctx, a.pos[0], &params)), x.stderr, &l); err != nil {
			return err
		}
		for _, e := range l.Items {
			if x.json {
				if err := x.printJSON(e); err != nil {
					return err
				}
				continue
			}
			if e.Type == gen.Dir {
				fmt.Fprintf(x.stdout, "dir\t-\t-\t%s\t%s/\n", e.Hash, e.Path)
				continue
			}
			var size int64
			var mode gen.FileMode
			if e.Size != nil {
				size = *e.Size
			}
			if e.Mode != nil {
				mode = *e.Mode
			}
			fmt.Fprintf(x.stdout, "file\t%o\t%d\t%s\t%s\n", mode, size, e.Hash, e.Path)
		}
		if l.NextCursor == nil {
			return nil
		}
		params.Cursor = l.NextCursor
	}
}

func (x *cli) cat(a args) error {
	if len(a.pos) != 2 {
		return usagef("usage: mfgraph cat <snapshot_id> <path>")
	}
	resp, err := x.c.GetSnapshotFile(x.ctx, a.pos[0], &gen.GetSnapshotFileParams{Path: a.pos[1]})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return serverError(resp, x.stderr)
	}
	_, err = io.Copy(x.stdout, resp.Body)
	return err
}

func (x *cli) diff(a args) error {
	if len(a.pos) != 1 {
		return usagef("usage: mfgraph diff <snapshot_id> [--base <snapshot_id>]")
	}
	params := gen.GetSnapshotDiffParams{}
	if b := a.flag("--base"); b != "" {
		params.Base = &b
	}
	for {
		var d gen.Diff
		if err := decode(call(x.c.GetSnapshotDiff(x.ctx, a.pos[0], &params)), x.stderr, &d); err != nil {
			return err
		}
		for _, e := range d.Items {
			if x.json {
				if err := x.printJSON(e); err != nil {
					return err
				}
				continue
			}
			fmt.Fprintf(x.stdout, "%s\t%s\n", e.Change, e.Path)
		}
		if d.NextCursor == nil {
			return nil
		}
		params.Cursor = d.NextCursor
	}
}

func (x *cli) restore(a args) error {
	if len(a.pos) != 1 || a.flag("--branch") == "" {
		return usagef("usage: mfgraph restore <snapshot_id> --branch <id> [--path <path>] [-m <message>]")
	}
	var s gen.Snapshot
	if err := decode(call(x.c.GetSnapshot(x.ctx, a.pos[0], &gen.GetSnapshotParams{})), x.stderr, &s); err != nil {
		return err
	}
	b, err := x.branch(s.WorkspaceId, a.flag("--branch"))
	if err != nil {
		return err
	}
	req := gen.RestoreRequest{BranchId: b.BranchId, ExpectedGeneration: b.Generation}
	if p, ok := a.flags["--path"]; ok {
		req.Path = &p[0]
	}
	if m := a.flag("-m", "--message"); m != "" {
		req.Message = &m
	}
	var r gen.CommitResult
	if err := decode(call(x.c.RestoreSnapshot(x.ctx, a.pos[0], &gen.RestoreSnapshotParams{}, req)), x.stderr, &r, http.StatusCreated); err != nil {
		return err
	}
	if x.json {
		return x.printJSON(r)
	}
	fmt.Fprintln(x.stdout, r.SnapshotId)
	return nil
}

// runClient runs one client command; it returns the exit status.
func runClient(ctx context.Context, cmd string, argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	type spec struct {
		valued, repeated []string
		run              func(*cli, args) error
	}
	specs := map[string]spec{
		"project":   {nil, nil, nil},
		"workspace": {[]string{"--name"}, nil, nil},
		"upload":    {nil, nil, (*cli).uploadCmd},
		"commit":    {[]string{"--branch", "-m", "--message"}, []string{"--delete"}, (*cli).commit},
		"log":       {[]string{"--branch"}, nil, (*cli).log},
		"ls":        {nil, nil, (*cli).ls},
		"cat":       {nil, nil, (*cli).cat},
		"diff":      {[]string{"--base"}, nil, (*cli).diff},
		"restore":   {[]string{"--branch", "--path", "-m", "--message"}, nil, (*cli).restore},
	}
	sp := specs[cmd]
	global := []string{"--url", "--token", "--prefix"}
	a, err := parseArgs(argv, append(global, sp.valued...), sp.repeated, []string{"--json"})
	if err != nil {
		fmt.Fprintf(stderr, "mfgraph %s: %v\n", cmd, err)
		return 2
	}
	switch cmd {
	case "project", "workspace":
		if len(a.pos) == 0 || a.pos[0] != "create" {
			fmt.Fprintf(stderr, "usage: mfgraph %s create ...\n", cmd)
			return 2
		}
		a.pos = a.pos[1:]
		if cmd == "project" {
			sp.run = (*cli).projectCreate
		} else {
			sp.run = (*cli).workspaceCreate
		}
	}
	c := conn{url: getenv(envURL), token: getenv(envToken), prefix: getenv(envPrefix)}
	if v := a.flag("--url"); v != "" {
		c.url = v
	}
	if v := a.flag("--token"); v != "" {
		c.token = v
	}
	if v := a.flag("--prefix"); v != "" {
		c.prefix = v
	}
	if c.url == "" {
		c.url = defaultURL
	}
	client, err := c.client()
	if err != nil {
		fmt.Fprintf(stderr, "mfgraph: %v\n", err)
		return 2
	}
	err = sp.run(&cli{ctx: ctx, c: client, stdout: stdout, stderr: stderr, json: a.switches["--json"]}, a)
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintln(stderr, ue.msg)
		return 2
	case errors.Is(err, errServer):
		return 1
	default:
		fmt.Fprintf(stderr, "mfgraph %s: %v\n", cmd, err)
		return 1
	}
}
