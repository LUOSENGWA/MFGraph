// Package migrations embeds the SQL migration files.
//
//   - kernel/: tables owned by the kernel (projects, principals). Executed by
//     standalone deployments only; a host platform provides its own.
//   - mysql/: the MFGraph module schema (MySQL dialect).
//
// Files are named NNNN_description.sql and are immutable once released: the
// migration runner refuses to continue when the checksum of an applied file
// changes. Numbers may have gaps; a gap can be filled later.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed kernel/*.sql mysql/*.sql
var files embed.FS

// Kernel returns the kernel bootstrap migrations.
func Kernel() fs.FS { return sub("kernel") }

// MySQL returns the MFGraph module migrations.
func MySQL() fs.FS { return sub("mysql") }

func sub(dir string) fs.FS {
	s, err := fs.Sub(files, dir)
	if err != nil {
		panic(err)
	}
	return s
}
