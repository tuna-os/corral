package cmd

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

const (
	powerShellPrefix = "powershell.exe -NoLogo -NoProfile -EncodedCommand "
	// Use cmd.exe's bound, not CreateProcess's larger limit: the focused
	// guest command field may be a cmd.exe prompt. Return is not part of it.
	powerShellCommandLimit = 8191
)

func typeInputArgs(cmd *cobra.Command, args []string) error {
	if cmd.Flags().Changed("file") {
		if len(args) != 1 {
			return fmt.Errorf("type requires a VM name and exactly one input source: text or --file PATH (use - for stdin)")
		}
		if typeFile == "" {
			return fmt.Errorf("--file requires a path or - for stdin")
		}
		return nil
	}
	if len(args) != 2 {
		return fmt.Errorf("type requires a VM name and exactly one input source: text or --file PATH (use - for stdin)")
	}
	return nil
}

// prepareTypeInput completes source and launcher validation before the backend
// preflights the keyboard mapping and sends any events. It never prints input.
func prepareTypeInput(cmd *cobra.Command, args []string) (string, int, error) {
	if err := typeInputArgs(cmd, args); err != nil {
		return "", 0, err
	}
	var source string
	if cmd.Flags().Changed("file") {
		var data []byte
		var err error
		if typeFile == "-" {
			data, err = io.ReadAll(cmd.InOrStdin())
		} else {
			data, err = os.ReadFile(typeFile)
		}
		if err != nil {
			return "", 0, fmt.Errorf("read type input: %w", err)
		}
		source = string(data)
	} else {
		source = args[1]
	}
	if !utf8.ValidString(source) {
		return "", 0, fmt.Errorf("type input must be valid UTF-8")
	}
	if !typePowerShell {
		return source, utf8.RuneCountInString(source), nil
	}

	source = strings.TrimPrefix(source, "\ufeff")
	characters, units := 0, 0
	for _, r := range source {
		characters++
		units++
		if r > 0xffff {
			units++
		}
		if len(powerShellPrefix)+base64.StdEncoding.EncodedLen(units*2) > powerShellCommandLimit {
			return "", 0, fmt.Errorf("PowerShell launcher exceeds the Windows cmd.exe command-line limit of %d characters; use a smaller script", powerShellCommandLimit)
		}
	}
	if source == "" {
		return "", 0, fmt.Errorf("PowerShell source must not be empty")
	}

	var launcher strings.Builder
	launcher.Grow(len(powerShellPrefix) + base64.StdEncoding.EncodedLen(units*2))
	launcher.WriteString(powerShellPrefix)
	encoder := base64.NewEncoder(base64.StdEncoding, &launcher)
	// Stream little-endian code units through the standard Base64 encoder:
	// no whole-source rune slice or intermediate UTF-16 copy is needed.
	var buf [4]byte
	for _, r := range source {
		n := 2
		if r <= 0xffff {
			binary.LittleEndian.PutUint16(buf[:2], uint16(r))
		} else {
			hi, lo := utf16.EncodeRune(r)
			binary.LittleEndian.PutUint16(buf[:2], uint16(hi))
			binary.LittleEndian.PutUint16(buf[2:], uint16(lo))
			n = 4
		}
		// strings.Builder.Write cannot fail.
		_, _ = encoder.Write(buf[:n])
	}
	_ = encoder.Close()
	return launcher.String(), characters, nil
}
