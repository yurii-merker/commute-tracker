package timezone

import (
	"fmt"
	"time"
)

var uk *time.Location

func Init() error {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		return fmt.Errorf("loading Europe/London timezone: %w", err)
	}
	uk = loc
	return nil
}

func UK() *time.Location {
	return uk
}

func Now() time.Time {
	return time.Now().In(uk)
}
