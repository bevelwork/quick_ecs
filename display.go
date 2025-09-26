package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Color constants
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
	ColorBold   = "\033[1m"
)

// color wraps a string with the specified color code
func color(text, colorCode string) string {
	return colorCode + text + ColorReset
}

// colorBold wraps a string with the specified color code and bold formatting
func colorBold(text, colorCode string) string {
	return colorCode + ColorBold + text + ColorReset
}

// printHeader prints the application header
func printHeader(privateMode bool, callerIdentity *sts.GetCallerIdentityOutput) {
	header := []string{
		color(strings.Repeat("-", 40), ColorBlue),
		"-- ECS Quick Manager --",
		color(strings.Repeat("-", 40), ColorBlue),
	}
	if !privateMode {
		header = append(header, fmt.Sprintf(
			"  Account: %s \n  User: %s",
			*callerIdentity.Account, *callerIdentity.Arn,
		))
		header = append(header, color(strings.Repeat("-", 40), ColorBlue))
	}

	fmt.Println(strings.Join(header, "\n"))
}

// colorClusterStatus returns the appropriate color for cluster status
func colorClusterStatus(status string) string {
	switch status {
	case "ACTIVE":
		return ColorGreen
	case "INACTIVE":
		return ColorRed
	case "PROVISIONING":
		return ColorYellow
	case "DEPROVISIONING":
		return ColorYellow
	default:
		return ColorWhite
	}
}

// colorServiceStatus returns the appropriate color for service status
func colorServiceStatus(status string) string {
	switch status {
	case "ACTIVE":
		return ColorGreen
	case "DRAINING":
		return ColorYellow
	case "INACTIVE":
		return ColorRed
	default:
		return ColorWhite
	}
}

// Progress indicator functions
var throbberChars = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// showProgress runs a throbber animation while executing a function
func showProgress(message string, fn func() error) error {
	done := make(chan error, 1)

	// Start the operation in a goroutine
	go func() {
		done <- fn()
	}()

	// Show throbber while waiting
	i := 0
	for {
		select {
		case err := <-done:
			// Clear the line and return
			fmt.Printf("\r\033[K")
			return err
		default:
			// Show throbber
			fmt.Printf("\r%s %s", throbberChars[i%len(throbberChars)], message)
			i++
			// Small delay to make throbber visible
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// showProgressWithResult runs a throbber animation while executing a function that returns a result
func showProgressWithResult[T any](message string, fn func() (T, error)) (T, error) {
	done := make(chan struct {
		result T
		err    error
	}, 1)

	// Start the operation in a goroutine
	go func() {
		result, err := fn()
		done <- struct {
			result T
			err    error
		}{result, err}
	}()

	// Show throbber while waiting
	i := 0
	for {
		select {
		case res := <-done:
			// Clear the line and return
			fmt.Printf("\r\033[K")
			return res.result, res.err
		default:
			// Show throbber
			fmt.Printf("\r%s %s", throbberChars[i%len(throbberChars)], message)
			i++
			// Small delay to make throbber visible
			time.Sleep(100 * time.Millisecond)
		}
	}
}
