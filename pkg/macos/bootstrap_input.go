package macos

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
)

// Qcodes follow pkg/qemu's US keyboard mapping. Only public Recovery shell
// commands use this transport: Kubernetes exec/audit must never receive vault
// fields, password hashes, authentication tokens or private keys.
var recoveryPlainQcodes = map[byte]string{
	' ': "spc", '-': "minus", '=': "equal", '[': "bracket_left", ']': "bracket_right",
	'\\': "backslash", ';': "semicolon", '\'': "apostrophe", ',': "comma",
	'.': "dot", '/': "slash", '`': "grave_accent",
}
var recoveryShiftedQcodes = map[byte]string{
	'!': "1", '@': "2", '#': "3", '$': "4", '%': "5", '^': "6", '&': "7",
	'*': "8", '(': "9", ')': "0", '_': "minus", '+': "equal", '{': "bracket_left",
	'}': "bracket_right", '|': "backslash", ':': "semicolon", '"': "apostrophe",
	'<': "comma", '>': "dot", '?': "slash", '~': "grave_accent",
}

func recoveryUSQcode(character byte) (string, bool) {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	const digits = "0123456789"
	switch {
	case character >= 'a' && character <= 'z':
		index := character - 'a'
		return letters[index : index+1], false
	case character >= 'A' && character <= 'Z':
		index := character - 'A'
		return letters[index : index+1], true
	case character >= '0' && character <= '9':
		index := character - '0'
		return digits[index : index+1], false
	}
	if code := recoveryPlainQcodes[character]; code != "" {
		return code, false
	}
	return recoveryShiftedQcodes[character], true
}

func typeRecoveryPublicQMP(ctx context.Context, target GuestTarget, vmUID, value string) error {
	if !printableUS(value) || vmUID == "" {
		return errors.New("public Recovery input requires printable US text and a bound VM identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	get := func(resource string) ([]byte, error) {
		query, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		return exec.CommandContext(query, "kubectl", "--context", target.KubeContext, "-n", target.Namespace, "get", resource, "-o", "json").Output()
	}
	body, err := get("vmi/" + target.Name)
	if err != nil {
		return errors.New("cannot inspect the owned Recovery VMI before public keyboard input")
	}
	var vmi object
	if json.Unmarshal(body, &vmi) != nil || !owned(vmi, target.Name) || nested(vmi, "status", "phase") != "Running" {
		return errors.New("public Recovery keyboard input requires the owned running VMI")
	}
	vmiUID, _ := nested(vmi, "metadata", "uid").(string)
	controlled := func(item object, kind, name, uid string) bool {
		refs, _ := nested(item, "metadata", "ownerReferences").([]any)
		for _, entry := range refs {
			ref, ok := entry.(map[string]any)
			if ok && ref["kind"] == kind && ref["name"] == name && ref["uid"] == uid && ref["controller"] == true {
				return true
			}
		}
		return false
	}
	if vmiUID == "" || !controlled(vmi, "VirtualMachine", target.Name, vmUID) {
		return errors.New("Recovery VMI no longer belongs to the bound VM")
	}
	body, err = get("pods")
	if err != nil {
		return errors.New("cannot locate the owned Recovery launcher pod")
	}
	var pods struct {
		Items []object `json:"items"`
	}
	if json.Unmarshal(body, &pods) != nil {
		return errors.New("cannot inspect Recovery launcher ownership")
	}
	pod := ""
	for _, item := range pods.Items {
		if !controlled(item, "VirtualMachineInstance", target.Name, vmiUID) || nested(item, "status", "phase") != "Running" {
			continue
		}
		name, _ := nested(item, "metadata", "name").(string)
		if name == "" || pod != "" {
			return errors.New("Recovery launcher ownership is ambiguous")
		}
		pod = name
	}
	if pod == "" {
		return errors.New("no running launcher is controlled by the selected Recovery VMI")
	}
	cmd := exec.CommandContext(ctx, "kubectl", "--context", target.KubeContext, "-n", target.Namespace, "exec", "-i", pod, "-c", "compute", "--", "bash", "-s")
	input, err := cmd.StdinPipe()
	if err != nil {
		return errors.New("cannot prepare the public Recovery keyboard stream")
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if cmd.Start() != nil {
		_ = input.Close()
		return errors.New("cannot open the owned Recovery QMP keyboard transport")
	}
	writer := bufio.NewWriterSize(input, 16384)
	writeErr := writeRecoveryPublicKeys(writer, target.Namespace+"_"+target.Name, value)
	if writeErr == nil {
		writeErr = writer.Flush()
	}
	closeErr := input.Close()
	if writeErr != nil || closeErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("public Recovery keyboard input was interrupted; no vault item was read")
	}
	if cmd.Wait() != nil {
		return errors.New("public Recovery QMP keyboard input failed; inspect the focused Recovery shell; no vault item was read")
	}
	return nil
}

func writeRecoveryPublicKeys(w io.Writer, domain, value string) error {
	if _, err := io.WriteString(w, "set -e\ndomain="+guestShellQuote(domain)+"\nmonitor() {\n    reply=$(virsh -c qemu:///session qemu-monitor-command \"$domain\" \"$1\") || exit 70\n    case \"$reply\" in *'\"error\"'*) exit 70;; *'\"return\"'*) ;; *) exit 70;; esac\n}\n"); err != nil {
		return err
	}
	// The acknowledged shell must be idle. Cancel abandoned public input,
	// including a secondary prompt, before starting a fresh protected command.
	if _, err := io.WriteString(w, `monitor '{"execute":"send-key","arguments":{"keys":[{"type":"qcode","data":"ctrl"},{"type":"qcode","data":"c"}],"hold-time":1}}'
sleep 0.04
`); err != nil {
		return err
	}
	for index := 0; index <= len(value); index++ {
		code, shifted := "ret", false
		if index < len(value) {
			code, shifted = recoveryUSQcode(value[index])
		}
		if code == "" {
			return errors.New("unsupported public US keyboard character")
		}
		if _, err := io.WriteString(w, `monitor '{"execute":"send-key","arguments":{"keys":[`); err != nil {
			return err
		}
		if shifted {
			if _, err := io.WriteString(w, `{"type":"qcode","data":"shift"},`); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, `{"type":"qcode","data":"`+code+`"}],"hold-time":1}}'
sleep 0.04
`); err != nil {
			return err
		}
	}
	return nil
}
