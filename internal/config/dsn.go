package config

import (
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// parseDSN parses a DSN of the MySQL driver (go-sql-driver/mysql). Its error
// names only the variable: the driver's message can quote a part of the DSN,
// for example the first half of a password that contains '/'.
func parseDSN(name, dsn string) (*mysql.Config, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("config: %s is not a valid DSN (the form is user:password@tcp(host:port)/database?param=value; "+
			"a password with / ? @ needs the database name)", name)
	}
	return cfg, nil
}

// ForwarderCharset is the character set of the event forwarder's
// connection: the envelopes are UTF-8 and actor references may hold any
// Unicode character, so nothing else can store them unchanged.
const ForwarderCharset = "utf8mb4"

// ForwarderDSN returns the connection settings of the event forwarder of
// module mode: those of MFGRAPH_OPS_DSN, or of MFGRAPH_DSN without it, with
// the character set utf8mb4. A DSN without the charset parameter gets
// charset=utf8mb4. A DSN that would give the connection another character
// set is refused: a charset other than utf8mb4 (also a list such as
// utf8,utf8mb4, whose second entry is only a fallback), a collation of
// another character set, or a character_set_* or collation_* session
// variable with another value. With charset=utf8 (utf8mb3) an actor with a
// character outside the BMP could never be delivered, and with latin1 the
// host table would receive mangled text. The error names the variable and
// the parameter, never the DSN.
func (c Config) ForwarderDSN() (*mysql.Config, error) {
	name, dsn := EnvOpsDSN, c.OpsDSN
	if dsn == "" {
		name, dsn = c.dbDSNVar(), c.DBDSN
	}
	cfg, err := parseDSN(name, dsn)
	if err != nil {
		return nil, err
	}
	refuse := func(what string) error {
		return fmt.Errorf("config: %s %s; the event forwarder writes the host's outbox table with the character set %s only: "+
			"remove the parameter or set charset=%s", name, what, ForwarderCharset, ForwarderCharset)
	}
	if v, ok := dsnParam(dsn, "charset"); ok && !strings.EqualFold(v, ForwarderCharset) {
		if strings.Contains(v, ",") {
			return nil, refuse("sets a list of character sets (charset=" + plainName(v) + ")")
		}
		return nil, refuse("sets the character set " + plainName(v))
	}
	if cfg.Collation != "" && !isUTF8MB4Collation(cfg.Collation) {
		return nil, refuse("sets the collation " + plainName(cfg.Collation) + " of another character set")
	}
	for k, v := range cfg.Params {
		v = strings.Trim(v, `'"`)
		switch k := strings.ToLower(k); {
		case strings.HasPrefix(k, "character_set_") && !strings.EqualFold(v, ForwarderCharset),
			strings.HasPrefix(k, "collation_") && !isUTF8MB4Collation(v):
			return nil, refuse("sets the session variable " + plainName(k) + " to another character set")
		}
	}
	if err := cfg.Apply(mysql.Charset(ForwarderCharset, cfg.Collation)); err != nil {
		return nil, err
	}
	return cfg, nil
}

// dsnParam returns the last value of a parameter of a DSN, as the driver
// reads it: the parameters follow the first '?' after the last '/'.
func dsnParam(dsn, key string) (string, bool) {
	i := strings.LastIndexByte(dsn, '/')
	if i < 0 {
		return "", false
	}
	_, query, ok := strings.Cut(dsn[i+1:], "?")
	if !ok {
		return "", false
	}
	value, found := "", false
	for _, kv := range strings.Split(query, "&") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			value, found = v, true
		}
	}
	return value, found
}

func isUTF8MB4Collation(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), ForwarderCharset+"_")
}

// plainName returns s if it looks like the name of a character set,
// collation or variable, and "(another value)" otherwise: messages never
// repeat arbitrary text from a DSN.
func plainName(s string) string {
	if len(s) == 0 || len(s) > 64 {
		return "(another value)"
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == ',') {
			return "(another value)"
		}
	}
	return s
}
