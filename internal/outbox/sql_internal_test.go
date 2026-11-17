package outbox

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestIsDuplicateOf: a duplicate key error counts as "delivered before" only
// when its quoted duplicate value is exactly the event's event_id, whatever
// the key is called and in whatever language the server reports it.
func TestIsDuplicateOf(t *testing.T) {
	id := strings.Repeat("ab", 32)
	other := strings.Repeat("cd", 32)
	dup := func(msg string) error {
		return fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1062, Message: msg})
	}
	yes := []error{
		dup("Duplicate entry '" + id + "' for key 'module_outbox.uk_module_outbox_event'"), // MySQL 8.0
		dup("Duplicate entry '" + id + "' for key 'uk_module_outbox_event'"),               // seekdb
		dup("Duplicate entry '" + id + "' for key 'module_outbox.uk_event_id'"),            // another name
		dup("Doppelter Eintrag '" + id + "' für Schlüssel 'uk_event_id'"),                  // lc_messages=de_DE
	}
	no := []error{
		dup("Duplicate entry '" + other + "' for key 'uk_event_id'"),
		dup("Duplicate entry 'mfgraph-" + id + "' for key 'uk_module_event'"), // a key of two columns
		dup("Duplicate entry '" + id + "-x' for key 'uk_x'"),
		&mysql.MySQLError{Number: 1061, Message: "Duplicate key name '" + id + "'"},
		errors.New("Duplicate entry '" + id + "' for key 'uk'"), // not a server error
		nil,
	}
	for _, err := range yes {
		if !isDuplicateOf(err, id) {
			t.Errorf("not recognized: %v", err)
		}
	}
	for _, err := range no {
		if isDuplicateOf(err, id) {
			t.Errorf("recognized: %v", err)
		}
	}
	if isDuplicateOf(dup("Duplicate entry '' for key 'k'"), "") {
		t.Error("an empty event_id matched")
	}
}
