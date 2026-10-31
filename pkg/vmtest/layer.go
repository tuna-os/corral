package vmtest

import "github.com/tuna-os/corral/pkg/image/layer"

// Layer engines.
const (
	EngineAuto    = "auto"
	EngineRemora  = "remora"
	EngineBuiltin = "builtin"
)

// PostBootUnit is the systemd unit the derived image runs once the guest is up.
const PostBootUnit = layer.PostBootUnit

// ReadyMarker is printed after the post-boot hook finishes, whatever it reported.
const ReadyMarker = layer.ReadyMarker

// HookOKMarker and HookFailMarker report the hook's own verdict on the console.
const (
	HookOKMarker   = layer.HookOKMarker
	HookFailMarker = layer.HookFailMarker
)

// StatusFile records the post-boot hook's exit code inside the guest.
const StatusFile = layer.StatusFile

// HookLogFile is the hook's own output inside the guest.
const HookLogFile = layer.HookLogFile

// HookTimeout bounds the post-boot hook.
const HookTimeout = layer.HookTimeout

// layerConfig translates manifest types and provision modes at the harness
// boundary. The layer package never imports the manifest DSL or VM machinery.
func (s *Spec) layerConfig() *layer.LayerConfig {
	config := &layer.LayerConfig{
		RootPassword: s.RootPassword,
		Packages:     s.Packages,
		ExtraRun:     s.ExtraRun,
		ImageScripts: s.imageScripts(),
		BootScripts:  s.bootScripts(),
	}
	for _, u := range s.Users {
		config.Users = append(config.Users, layer.User{
			Name: u.Name, Password: u.Password, Sudo: u.Sudo,
			Groups: u.Groups, Shell: u.Shell, SSHAuthorizedKeys: u.SSHAuthorizedKeys,
		})
	}
	for _, f := range s.Files {
		config.Files = append(config.Files, layer.File{Path: f.Path, Content: f.Content, Mode: f.Mode})
	}
	return config
}
