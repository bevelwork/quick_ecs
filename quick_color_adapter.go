package main

import (
	"os"
	"time"

	qc "github.com/bevelwork/quick_color"
)

// Spinner adapters matching prior API
var throbberChars = qc.DefaultSpinnerFrames

func startThrobber(message string) (stop func()) {
	sp := qc.NewSpinner(os.Stdout, message, 100*time.Millisecond)
	sp.Start()
	return sp.Stop
}

func showProgress(message string, fn func() error) error {
	_, err := qc.WithProgress[struct{}](os.Stdout, message, 100*time.Millisecond, func() (struct{}, error) {
		return struct{}{}, fn()
	})
	return err
}

func showProgressWithResult[T any](message string, fn func() (T, error)) (T, error) {
	return qc.WithProgress[T](os.Stdout, message, 100*time.Millisecond, fn)
}
