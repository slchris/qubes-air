//go:build !linux

package desktop

import (
	"context"
	"errors"
)

// Run fails closed on hosts without Linux peer credentials and the desktop unit.
func Run(context.Context) error {
	return errors.New("desktop broker requires Linux")
}
