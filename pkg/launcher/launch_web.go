package launcher

import (
	"net/http"

	"github.com/tuna-os/corral/pkg/web"
)

func defaultWebHandler() (http.Handler, error) {
	return web.NewHandler()
}
