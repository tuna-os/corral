package vmtest

import (
	"github.com/tuna-os/corral/pkg/image/layer"
)

// Re-exported constants from pkg/image/layer for backwards compatibility.
const (
	EngineAuto    = layer.EngineAuto
	EngineRemora  = layer.EngineRemora
	EngineBuiltin = layer.EngineBuiltin

	PostBootUnit   = layer.PostBootUnit
	ReadyMarker    = layer.ReadyMarker
	HookOKMarker   = layer.HookOKMarker
	HookFailMarker = layer.HookFailMarker
	StatusFile     = layer.StatusFile
	HookLogFile    = layer.HookLogFile
	HookTimeout    = layer.HookTimeout
)

// Re-exported types from pkg/image/layer.
type (
	BuildContext   = layer.BuildContext
	ContextFile    = layer.ContextFile
	PackageManager = layer.PackageManager
)

// Re-exported functions from pkg/image/layer.
var (
	NewBuildContext      = layer.NewBuildContext
	AccountScript        = layer.AccountScript
	ServiceScript        = layer.ServiceScript
	PostBootScript       = layer.PostBootScript
	PostBootUnitFile     = layer.PostBootUnitFile
	BuiltinContainerfile = layer.BuiltinContainerfile
	RemoraManifest       = layer.RemoraManifest
	HashPassword         = layer.HashPassword
	CryptSalt            = layer.CryptSalt
	ShellQuote           = layer.ShellQuote
	Shebang              = layer.Shebang
)
