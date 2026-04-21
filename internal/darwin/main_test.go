package darwin

import (
	"os"
	"testing"

	"github.com/yurii-merker/commute-tracker/internal/timezone"
)

func TestMain(m *testing.M) {
	if err := timezone.Init(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
