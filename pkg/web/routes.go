package web

import (
	"fmt"
	"io/fs"
	"net/http"

	"golang.org/x/net/websocket"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/proxmox"
)

// newMux builds the HTTP router wrapped in the request pipeline. Split out
// from Serve so tests can exercise the full route table with httptest.
func newMux() (http.Handler, error) {
	// Capture this for the router being built, then clear it so a demo mux in
	// one test cannot turn later production-style muxes into demo consoles.
	useDemoConsole := demoEnabled
	demoEnabled = false
	mux, err := routes(useDemoConsole)
	if err != nil {
		return nil, err
	}
	return chain(mux, requestPipeline()...), nil
}

// routes builds the route table. useDemoConsole serves the demo VNC console
// instead of dialing a real VM.
func routes(useDemoConsole bool) (*http.ServeMux, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	// Static files (css, js, vendor/) served from the embedded FS.
	// Index.html is intercepted and theme-injected by the serveRoot wrapper.
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			serveIndex(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	// Proxmox API compatibility — expose /api2/json/… alongside the
	// corral REST API so ecosystem tools can manage VMs through the
	// same corral web deployment.
	mux.Handle("/api2/json/", proxmox.NewHandler(kubevirt.DefaultNamespace))

	// /metrics sits outside /api because that is where scrapers look. It is
	// behind the same admin gate as everything else, so a tailnet-gated Corral
	// has a tailnet-gated metrics endpoint (ADR-0011).
	mux.HandleFunc("GET /metrics", handleMetricsExposition)

	// Health and readiness endpoints for orchestrator probes (liveness/readiness).
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", handleReadyz)

	mux.HandleFunc("GET /api/whoami", handleWhoami)
	mux.HandleFunc("GET /api/theme", handleGetTheme)
	mux.HandleFunc("PUT /api/theme", handlePutTheme)
	mux.HandleFunc("GET /api/vms", handleListVMs)
	mux.HandleFunc("GET /api/v1/inventory", handleInventory)
	mux.HandleFunc("GET /api/v1/meta", handleAPIMeta)
	mux.HandleFunc("GET /api/contexts", handleContexts)
	mux.HandleFunc("POST /api/vms", handleCreateVM)
	mux.HandleFunc("GET /api/cts", handleListCTs)
	mux.HandleFunc("POST /api/cts", handleCreateCT)
	mux.HandleFunc("POST /api/cts/{ns}/{name}/{action}", handleCTAction)
	mux.HandleFunc("DELETE /api/cts/{ns}/{name}", handleDeleteCT)
	mux.HandleFunc("PUT /api/cts/{ns}/{name}/scale", handleScaleCT)
	mux.HandleFunc("GET /api/nodes", handleNodes)
	mux.HandleFunc("GET /api/nodes/{name}/metrics/history", handleNodeMetricsHistory)
	mux.HandleFunc("GET /api/metrics/history", handleDCMetricsHistory)
	mux.HandleFunc("GET /api/metrics/top", handleTopVMs)
	mux.HandleFunc("GET /api/hostpower", handleHostPower)
	mux.HandleFunc("POST /api/hostpower/{plugin}/{action}", handleHostPowerAction)
	mux.HandleFunc("GET /api/capabilities", handleCapabilities)
	mux.HandleFunc("GET /api/images", handleImages)
	mux.HandleFunc("GET /api/sources", handleListSources)
	mux.HandleFunc("POST /api/sources", handleAddSource)
	mux.HandleFunc("DELETE /api/sources/{name}", handleDeleteSource)
	mux.HandleFunc("GET /api/instancetypes", handleInstanceTypes)
	mux.HandleFunc("GET /api/nads", handleNADs)
	mux.HandleFunc("GET /api/gpus", handleListGPUs)
	mux.HandleFunc("GET /api/vms/{ns}/{name}/gpus", handleGetVMGPUs)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/gpus", handleAttachGPU)
	mux.HandleFunc("DELETE /api/vms/{ns}/{name}/gpus/{gpu}", handleDetachGPU)
	mux.HandleFunc("GET /api/doctor", handleDoctor)
	mux.HandleFunc("POST /api/doctor/fix", handleDoctorFix)
	mux.HandleFunc("GET /api/plugins", handlePlugins)
	mux.HandleFunc("POST /api/plugins/{name}/install", handleInstallPlugin)
	mux.HandleFunc("DELETE /api/plugins/{name}", handleRemovePlugin)
	mux.HandleFunc("GET /api/plugins/ui", handlePluginUI)
	mux.HandleFunc("GET /api/plugins/{plugin}/ui/{id}", handlePluginUIItem)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/nics", handleAddNIC)
	mux.HandleFunc("GET /api/datavolumes", handleListDataVolumes)
	mux.HandleFunc("POST /api/datavolumes", handleImportDataVolume)
	mux.HandleFunc("POST /api/datavolumes/upload", handleUploadDataVolume)
	mux.HandleFunc("DELETE /api/datavolumes/{ns}/{name}", handleDeleteDataVolume)
	mux.HandleFunc("GET /api/tasks/{id}", handleTaskStatus)
	mux.HandleFunc("GET /api/tasklog", handleTaskLog)
	registerFolderRoutes(mux)
	registerMoveRoutes(mux)
	if vdiBroker, err = newVDIBroker(); err != nil {
		return nil, err
	}
	registerVDIRoutes(mux)
	mux.HandleFunc("GET /api/vms/{ns}/{name}", handleVMInfo)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/{action}", handleVMAction)
	mux.HandleFunc("DELETE /api/vms/{ns}/{name}", handleDeleteVM)

	// Cluster-only operations. More specific patterns win over {action} above.
	mux.HandleFunc("POST /api/vms/{ns}/{name}/migrate", noLocal(handleMigrate))
	mux.HandleFunc("POST /api/vms/{ns}/{name}/scale", noLocal(handleScale))
	mux.HandleFunc("POST /api/vms/{ns}/{name}/expand", noLocal(handleExpand))
	mux.HandleFunc("POST /api/vms/{ns}/{name}/clone", noLocal(handleClone))
	mux.HandleFunc("POST /api/vms/{ns}/{name}/template", noLocal(handleMarkTemplate))
	mux.HandleFunc("POST /api/vms/{ns}/{name}/tags", noLocal(handleSetTag))
	mux.HandleFunc("POST /api/vms/{ns}/{name}/options", noLocal(handleSetOptions))
	mux.HandleFunc("GET /api/vms/{ns}/{name}/guestinfo", handleGuestInfo)
	mux.HandleFunc("GET /api/vms/{ns}/{name}/rdp", handleRDPCheck)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/bootc/rebuild", noLocal(handleBootcRebuild))
	mux.HandleFunc("GET /api/vms/{ns}/{name}/events", handleEvents)
	mux.HandleFunc("GET /api/vms/{ns}/{name}/metrics", handleMetrics)
	mux.HandleFunc("GET /api/vms/{ns}/{name}/metrics/history", handleMetricsHistory)
	mux.HandleFunc("GET /api/vms/{ns}/{name}/export", noLocal(handleExport))
	mux.HandleFunc("POST /api/vms/{ns}/{name}/volumes", noLocal(handleAddVolume))
	mux.HandleFunc("DELETE /api/vms/{ns}/{name}/volumes/{vol}", noLocal(handleRemoveVolume))
	mux.HandleFunc("GET /api/vms/{ns}/{name}/snapschedule", handleGetSnapSchedule)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/snapschedule", noLocal(handleSetSnapSchedule))
	mux.HandleFunc("DELETE /api/vms/{ns}/{name}/snapschedule", noLocal(handleDeleteSnapSchedule))
	mux.HandleFunc("GET /api/vms/{ns}/{name}/powerschedule", handleGetPowerSchedule)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/powerschedule", noLocal(handleSetPowerSchedule))
	mux.HandleFunc("DELETE /api/vms/{ns}/{name}/powerschedule", noLocal(handleDeletePowerSchedule))
	mux.HandleFunc("GET /api/vms/{ns}/{name}/snapshots", handleListSnapshots)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/snapshots", handleCreateSnapshot)
	mux.HandleFunc("DELETE /api/vms/{ns}/{name}/snapshots/{snap}", handleDeleteSnapshot)
	mux.HandleFunc("POST /api/vms/{ns}/{name}/snapshots/{snap}/restore", handleRestoreSnapshot)

	vncHandler := websocket.Handler(vncBridge)
	if useDemoConsole {
		vncHandler = func(ws *websocket.Conn) {
			defer func() { _ = ws.Close() }()
			serveDemoVNC(ws)
		}
	}
	mux.Handle("GET /api/vnc/{ns}/{name}", consoleWebSocket(trackConsole(vncHandler)))
	mux.Handle("GET /api/tty/{ns}/{name}", consoleWebSocket(trackConsole(ttyBridge)))
	mux.Handle("GET /api/rdp/{ns}/{name}", consoleWebSocket(trackConsole(rdpBridge)))

	return mux, nil
}

// noLocal guards a cluster-only operation. Such operations have no meaning for
// local QEMU VMs (#91), so the reserved "local" namespace gets a uniform 400
// instead of a confusing kubectl error.
func noLocal(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("ns") == localNS {
			errResp(w, http.StatusBadRequest,
				fmt.Errorf("this operation is not supported for local QEMU VMs"))
			return
		}
		h(w, r)
	}
}

// consoleWebSocket serves a console bridge over a websocket whose Origin
// passes validateWebSocketOrigin.
func consoleWebSocket(h websocket.Handler) http.Handler {
	return websocket.Server{
		Handler: h,
		Handshake: func(cfg *websocket.Config, r *http.Request) error {
			return validateWebSocketOrigin(r)
		},
	}
}

// serveIndex reads the embedded index.html, injects the active theme, and
// serves it. This is the only dynamic page — everything else is static.
func serveIndex(w http.ResponseWriter, r *http.Request) {
	html, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(injectTheme(string(html))))
}
