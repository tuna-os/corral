package web

// A plugin UI source for demo mode.
//
// `corral web --demo` installs no plugins, so without this the browser suite
// could never see a plugin widget or section. Like the host-power fixture,
// this lives behind demoMode and is not a plugin: it answers with documents
// built here instead of running a binary. Everything after that - the cache,
// the API and the browser's rendering - is the path a real plugin takes.
//
// One row carries markup on purpose. The suite checks that the page shows it
// as text, which is the property the whole contract depends on.

import (
	"fmt"

	"github.com/tuna-os/corral/pkg/plugin/sdk"
)

const demoPluginUIName = "demo-ui"

func demoPluginUISource() uiSource {
	return uiSource{name: demoPluginUIName, ui: &sdk.UI{
		Widgets: []sdk.UIItem{
			{ID: "spend", Title: "Cloud spend", Icon: "chart", Command: "ui spend"},
		},
		Sections: []sdk.UIItem{
			{ID: "backups", Title: "Backups", Icon: "disk", Command: "ui backups"},
			{ID: "offline", Title: "Offline report", Command: "ui offline"},
		},
	}}
}

func demoPluginUIDocument(id string) (sdk.Document, error) {
	switch id {
	case "spend":
		return sdk.ParseDocument([]byte(`{"kind":"rows","rows":[
			{"label":"This month","value":"$412.08","state":"ok"},
			{"label":"Forecast","value":"$530.00","state":"warn"},
			{"label":"Note","value":"<img src=x onerror=alert(1)>","state":"muted"}
		]}`))
	case "backups":
		return sdk.ParseDocument([]byte(`{"kind":"table",
			"columns":["Guest","Last backup","Size"],
			"rows":[["web-1","02:00 today","4.1 GiB"],["db-1","02:10 today","18.7 GiB"],["ci-runner","3 days ago","2.2 GiB"]]}`))
	case "offline":
		return sdk.Document{}, fmt.Errorf("the plugin failed: report server unreachable")
	}
	return sdk.Document{}, fmt.Errorf("no such item %q", id)
}
