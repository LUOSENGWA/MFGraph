package config

import (
	"fmt"

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
