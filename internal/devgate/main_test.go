package devgate

import (
	"os"
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// The suite is a Russian connector, whatever the machine running it speaks: a caller that
// names no language gets the connector's own, and the tests say which that is.
func TestMain(m *testing.M) {
	i18n.SetDefault(i18n.RU)
	os.Exit(m.Run())
}
